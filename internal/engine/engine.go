// Package engine reimplements SteelSeries Engine (SteelSeriesEngine.exe):
// device discovery, the GoLisp device runtime, configurations and the
// Engine API used by the client.
package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"ggl/internal/db"
	"ggl/internal/devicert"
	"ggl/internal/events"
	"ggl/internal/hid"
	"ggl/internal/httpx"
	"ggl/internal/paths"
	"ggl/internal/store"
)

type Engine struct {
	srv *httpx.Server
	gs  *httpx.Server
	hub *events.Hub // wss://<engine>/sock

	db    *sql.DB
	state *store.Store // gg-linux bookkeeping the Engine schema has no place for
	rt    *devicert.Runtime
	hid   *hid.Manager

	mu         sync.Mutex
	addr       string
	gsAddr     string
	broadcast  func(event string, data any)
	live       map[int]*liveDevice // by catalog device id
	prism      prismState
	prismInput prismInputState
	soft       softState
	gameSense  *gameSense
	imageSync  *imageSync
	userID     int
	appSet     map[string]any

	deviceEvents chan deviceEvent
	sequencer    *sequencer
	subApps      func() any
	trayChanged  func()

	mmDevices     map[uint32]mmDevice // headsets, by USB audio device
	onChatMix     func(audio uint32, game, chat int)
	onHeadsetLink func()
	onBatteryLow  func(device string, percent int, sound bool)
	deploying     map[uint32]int // deploys in progress, by physical device
	autoFlatten   func() bool    // Sonar handles EQ (see flatten.go)

	onConfigSelected func(deviceID int, configID string) // see quickset.go
}

// SetSubAppSource lets the Engine's /subApps mirror the GG core's list;
// the device config window reads the PrismSync address from it.
func (e *Engine) SetSubAppSource(fn func() any) {
	e.mu.Lock()
	e.subApps = fn
	e.mu.Unlock()
}

// engineSubApps are the sub-apps the Engine hosts (PrismSync), as GG
// lists them.
func (e *Engine) engineSubApps() map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	e.mu.Lock()
	fn := e.subApps
	e.mu.Unlock()
	if fn == nil {
		return out
	}
	var all map[string]json.RawMessage
	b, _ := json.Marshal(fn())
	json.Unmarshal(b, &all)
	if v, ok := all[prismSubApp]; ok {
		out[prismSubApp] = v
	}
	return out
}

const prismSubApp = "prismSyncV2"

// replayEngineSubApps tells a client that connects to the Engine socket
// where PrismSync runs; the announcement made when it started is long
// gone by then.
func (e *Engine) replayEngineSubApps(send func(event string, data any)) {
	for name, raw := range e.engineSubApps() {
		var a struct {
			IsRunning bool            `json:"isRunning"`
			IsEnabled bool            `json:"isEnabled"`
			Metadata  json.RawMessage `json:"metadata"`
		}
		if json.Unmarshal(raw, &a) != nil || !a.IsRunning {
			continue
		}
		send("EVENT_ENGINE_SUB_APP_READY", map[string]any{"subAppName": name, "metadata": a.Metadata, "isEnabled": a.IsEnabled})
	}
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
	if e.state, err = store.Open(filepath.Join(paths.State(), "engine.json")); err != nil {
		return nil, err
	}
	if err := e.ensureUser(); err != nil {
		return nil, err
	}
	e.hid = hid.NewManager(e.onHotplug)
	e.rt, err = devicert.New(paths.DeviceSpecs(), paths.EngineRoot(), NewTransport(e.hid), e)
	if err != nil {
		return nil, err
	}
	if err := e.rt.LoadMigrations(paths.Migrations()); err != nil {
		slog.Warn("configuration migrations", "err", err)
	}
	e.routes()
	e.hub.OnAuthenticated(e.replayEngineSubApps)
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
	b, tray := e.broadcast, e.trayChanged
	e.mu.Unlock()
	if len(event) > 6 && event[:6] == "EVENT_" {
		b(event, data)
	}
	if tray != nil && trayEvents[event] {
		tray()
	}
}

// SubAppEvent forwards GG sub-app lifecycle events to engine socket
// clients (the device config windows listen there).
func (e *Engine) SubAppEvent(event string, data any) { e.hub.Broadcast(event, data) }

// Run polls for hotplug and dispatches device events until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	go e.eventLoop(ctx)
	e.imageSync.start(ctx)
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
	e.closeSoft()
	e.hid.Close()
	e.db.Close()
}

// devicert.Hooks

func (e *Engine) Log(devID uint32, msg string) { slog.Debug("device log", "id", devID, "msg", msg) }

// Deploying reports whether a configuration is being written to a
// physical device.
func (e *Engine) Deploying(devID uint32) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.deploying[devID] > 0
}

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
