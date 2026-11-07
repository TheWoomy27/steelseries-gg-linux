// Package engine reimplements SteelSeries Engine (SteelSeriesEngine.exe):
// device discovery, the GoLisp device runtime, configurations and the
// Engine API used by the client.
package engine

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"ggl/internal/db"
	"ggl/internal/devicert"
	"ggl/internal/events"
	"ggl/internal/hid"
	"ggl/internal/httpx"
	"ggl/internal/paths"
)

type Engine struct {
	srv *httpx.Server
	gs  *httpx.Server
	hub *events.Hub // wss://<engine>/sock

	db  *sql.DB
	rt  *devicert.Runtime
	hid *hid.Manager

	mu        sync.Mutex
	addr      string
	gsAddr    string
	broadcast func(event string, data any)
	live      map[int]*liveDevice // by catalog device id
	userID    int
	appSet    map[string]any

	deviceEvents chan deviceEvent
	sequencer    *sequencer
}

func New(ctx context.Context) (*Engine, error) {
	database, err := db.Open(paths.EngineDB(), paths.EngineDBMig())
	if err != nil {
		return nil, err
	}
	e := &Engine{
		srv:          httpx.NewServer("engine", ""),
		gs:           httpx.NewServer("gamesense", ""),
		hub:          events.NewHub("engine-sock", ""),
		db:           database,
		live:         map[int]*liveDevice{},
		broadcast:    func(string, any) {},
		deviceEvents: make(chan deviceEvent, 64),
		sequencer:    newSequencer(),
	}
	if err := e.ensureUser(); err != nil {
		return nil, err
	}
	e.hid = hid.NewManager(e.onHotplug)
	e.rt, err = devicert.New(paths.DeviceSpecs(), paths.EngineRoot(), NewTransport(e.hid), e)
	if err != nil {
		return nil, err
	}
	e.routes()
	e.gameSenseRoutes()
	return e, nil
}

func (e *Engine) Handler() http.Handler          { return e.srv }
func (e *Engine) GameSenseHandler() http.Handler { return e.gs }

func (e *Engine) SetAddresses(addr, gsAddr string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.addr, e.gsAddr = addr, gsAddr
}

// SetBroadcaster connects engine events to the GG core's sockets.
func (e *Engine) SetBroadcaster(fn func(event string, data any)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.broadcast = fn
}

// emit sends an event to the engine socket and, for EVENT_* names, the GG
// core sockets as well.
func (e *Engine) emit(event string, data any) {
	e.hub.Broadcast(event, data)
	e.mu.Lock()
	b := e.broadcast
	e.mu.Unlock()
	if len(event) > 6 && event[:6] == "EVENT_" {
		b(event, data)
	}
}

// SubAppEvent forwards GG sub-app lifecycle events to engine socket
// clients (the device config windows listen there).
func (e *Engine) SubAppEvent(event string, data any) { e.hub.Broadcast(event, data) }

// Run polls for hotplug and dispatches device events until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	go e.eventLoop(ctx)
	e.hid.Scan()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.hid.Scan()
		}
	}
}

func (e *Engine) Shutdown() {
	e.mu.Lock()
	lives := make([]*liveDevice, 0, len(e.live))
	for _, l := range e.live {
		lives = append(lives, l)
	}
	e.mu.Unlock()
	for _, l := range lives {
		for _, d := range l.decls() {
			if d.Shutdown != nil {
				done := make(chan struct{})
				go func() { d.RunShutdown(); close(done) }()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
				}
			}
		}
	}
	e.hid.Close()
	e.db.Close()
}

// devicert.Hooks

func (e *Engine) Log(devID uint32, msg string) { slog.Debug("device log", "id", devID, "msg", msg) }

func (e *Engine) ActiveConfigSettings(devID uint32) map[string]any {
	l := e.liveByProduct(devID)
	if l == nil {
		return map[string]any{}
	}
	cfg, err := e.activeConfig(l.dbID)
	if err != nil {
		return map[string]any{}
	}
	return cfg.settingsMap()
}
