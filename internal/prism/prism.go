// Package prism reimplements the PrismSync V2 service (SteelSeriesPrism.exe),
// GG's Prism lighting: configurations that light every supporting device
// together, their presets, which devices take part and where they sit,
// previews for the client, and the frames sent to the devices.
package prism

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"ggl/internal/db"
	"ggl/internal/devicert"
	"ggl/internal/engine"
	"ggl/internal/events"
	"ggl/internal/httpx"
)

// Engine is what PrismSync needs from the Engine: the devices it can light
// and a way to send them frames.
type Engine interface {
	PrismSyncDevices() []engine.PrismDevice
	PrismWrite(id int, writes []devicert.PrismWrite) error
	PrismRelease(id int)
	OnPrismDevicesChanged(fn func())
	WatchPrismInput(fn func(engine.PrismInput))
}

type Service struct {
	srv  *httpx.Server
	hub  *events.Hub
	db   *sql.DB
	eng  Engine
	emit func(event string, data any)
	kick chan struct{}
	done chan struct{}

	mu       sync.Mutex
	devices  map[int]*device // connected, by Engine id
	lit      map[int]bool    // devices being sent frames
	started  time.Time       // when the current configuration began
	current  *configRow
	previews map[string]preview // by config id and mode

	// Input, for the idle and reactive layers.
	watching  bool
	lastInput time.Time
	reactions []reaction

	audio audioCapture // for the Audio Visualizer
}

func New(dbPath, migrations string, eng Engine, emit func(string, any)) (*Service, error) {
	d, err := db.Open(dbPath, migrations)
	if err != nil {
		return nil, err
	}
	s := &Service{srv: httpx.NewServer("prism", ""), hub: events.NewHub("prism-sock", ""), db: d, eng: eng, emit: emit,
		kick: make(chan struct{}, 1), done: make(chan struct{}), devices: map[int]*device{}, lit: map[int]bool{}, started: time.Now(), previews: map[string]preview{}, lastInput: time.Now()}
	s.routes()
	if eng != nil {
		eng.OnPrismDevicesChanged(s.refreshDevices)
	}
	return s, nil
}

func (s *Service) Handler() http.Handler { return s.srv }

// Run sends frames to the devices PrismSync lights until ctx ends; then it
// gives them their own lighting back. Wait waits for that.
func (s *Service) Run(ctx context.Context) {
	defer close(s.done)
	s.refreshDevices()
	s.renderLoop(ctx)
}

// Wait returns once Run has finished.
func (s *Service) Wait() {
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
	}
}

// configRow is a stored configuration.
type configRow struct {
	ID           string
	Name         string
	IsCurrent    bool
	IsDeleted    bool
	IsHidden     bool
	IsLastDeploy bool
	IsPerDevice  bool
	Schema       int
	Data         ConfigData
}

func (s *Service) configs(where string, args ...any) []configRow {
	rows, err := s.db.Query(`SELECT id, name, is_current, is_deleted, is_hidden_immutable, is_last_manually_deployed,
		is_per_device, schema, config_json FROM configs WHERE `+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []configRow{}
	for rows.Next() {
		var c configRow
		var cur, del, hid, last, per int
		var data string
		if rows.Scan(&c.ID, &c.Name, &cur, &del, &hid, &last, &per, &c.Schema, &data) != nil {
			continue
		}
		c.IsCurrent, c.IsDeleted, c.IsHidden, c.IsLastDeploy, c.IsPerDevice = cur != 0, del != 0, hid != 0, last != 0, per != 0
		c.Data = parseConfig(data)
		out = append(out, c)
	}
	return out
}

func (s *Service) config(id string) *configRow {
	cs := s.configs(`id = ?`, id)
	if len(cs) == 0 {
		return nil
	}
	return &cs[0]
}

func (s *Service) currentConfig() *configRow {
	cs := s.configs(`is_current = 1 AND is_deleted = 0`)
	if len(cs) == 0 {
		return nil
	}
	return &cs[0]
}

func (s *Service) saveConfig(c *configRow) error {
	b, err := json.Marshal(c.Data)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE configs SET config_json = ?, name = ? WHERE id = ?`, string(b), c.Name, c.ID)
	s.configChanged(c.ID)
	return err
}

// configChanged drops the configuration's previews and, when it is the
// one lighting the devices, restarts it.
func (s *Service) configChanged(id string) {
	s.mu.Lock()
	for _, m := range []string{modeBase, modeIdle, modeReactive} {
		delete(s.previews, id+"/"+m)
	}
	if s.current != nil && s.current.ID == id {
		s.current = nil
	}
	s.mu.Unlock()
	s.reconcile()
}

// frontend renders configuration_types.ConfigurationDataForFrontend.
func (s *Service) frontend(c *configRow) map[string]any {
	devs := s.connected()
	zones, graphics := frontendZones(&c.Data, devs)
	return map[string]any{
		"id": c.ID, "name": c.Name, "is_current": c.IsCurrent, "idleTimeMs": c.Data.IdleTimeMs,
		"zones": zones, "graphics": graphics,
		"basePreview": s.preview(c, modeBase, devs), "idlePreview": s.preview(c, modeIdle, devs), "reactivePreview": s.preview(c, modeReactive, devs),
	}
}

func (s *Service) setting(key string) string {
	var v string
	s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v
}

func (s *Service) leftHanded() bool {
	v := s.setting("leftHanded")
	return v == "true" || v == "1"
}

func uuid() string {
	return httpx.RandomToken(4) + "-" + httpx.RandomToken(2) + "-" + httpx.RandomToken(2) + "-" + httpx.RandomToken(2) + "-" + httpx.RandomToken(6)
}

func logBody(route string, b []byte) {
	slog.Debug("prism request", "route", route, "body", string(b))
}
