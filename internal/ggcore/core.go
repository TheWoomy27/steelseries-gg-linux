// Package ggcore reimplements the GG core service (SteelSeriesGG.exe): the
// API the client uses for the user session, settings, sub-apps, loadouts,
// home page content and events.
package ggcore

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync"

	"ggl/internal/engine"
	"ggl/internal/events"
	"ggl/internal/httpx"
	"ggl/internal/paths"
	"ggl/internal/store"
)

type Core struct {
	srv   *httpx.Server
	sock  *events.Hub // /sock: Electron main process
	evts  *events.Hub // /eventing: renderer
	eng   *engine.Engine
	db    *store.Store
	token string

	mu          sync.Mutex
	ggAddr      string
	subApps     map[string]*SubApp
	subAppHooks map[string]func(enabled bool)
	games       gameLibrary
	taskbar     taskbar
	quickset    quicksetHooks
	// The services following the game being played, and its unique id.
	gameWatchers []gameWatcher
	playing      string
	shortcuts    shortcutStore
}

// OnSubAppToggle registers the in-process service behind a sub-app so it
// can start or stop when the user toggles it in GG settings.
func (c *Core) OnSubAppToggle(name string, fn func(enabled bool)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subAppHooks == nil {
		c.subAppHooks = map[string]func(bool){}
	}
	c.subAppHooks[name] = fn
}

// SubAppEnabled reports whether the user has a sub-app switched on.
func (c *Core) SubAppEnabled(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.subApps[name]
	return a != nil && a.IsEnabled
}

func New(ctx context.Context, token string, eng *engine.Engine) (*Core, error) {
	db, err := store.Open(filepath.Join(paths.State(), "gg.json"))
	if err != nil {
		return nil, err
	}
	c := &Core{
		srv:   httpx.NewServer("gg", token),
		sock:  events.NewHub("gg-sock", token),
		evts:  events.NewHub("gg-eventing", token),
		eng:   eng,
		db:    db,
		token: token,
	}
	c.initSubApps()
	c.sock.OnAuthenticated(c.replaySubApps)
	c.evts.OnAuthenticated(c.replaySubApps)
	eng.SetSubAppSource(func() any { return c.SubApps() })
	c.srv.Public("/healthCheck")
	c.srv.Public("/analytics/track")
	c.routes()
	eng.SetBroadcaster(c.Broadcast)
	return c, nil
}

func (c *Core) Handler() http.Handler { return c.srv }

func (c *Core) SetAddresses(gg, eng string) {
	c.mu.Lock()
	c.ggAddr = gg
	c.mu.Unlock()
	c.subAppReady("engine", SubAppMetadata{EncryptedWebServerAddress: eng})
}

// Broadcast sends an event to both the main process and renderer sockets.
func (c *Core) Broadcast(event string, data any) {
	c.sock.Broadcast(event, data)
	c.evts.Broadcast(event, data)
}

func (c *Core) Run(ctx context.Context) {
	c.eng.OnConfigSelected(c.selectedOutsideQuickset)
	go c.watchGames(ctx)
	<-ctx.Done()
}

func (c *Core) routes() {
	s := c.srv
	s.Handle("GET /healthCheck", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"healthCheck": map[string]bool{"status": true, "db_read": true}})
	})
	s.Handle("GET /sock", c.sock.ServeHTTP)
	s.Handle("GET /eventing", c.evts.ServeHTTP)

	c.subAppRoutes()
	c.userRoutes()
	c.settingsRoutes()
	c.contentRoutes()
	c.loadoutRoutes()
	c.quicksetRoutes()
	c.alertRoutes()
	c.autoclipRoutes()
	c.shortcutRoutes()
	c.gameRoutes()
	c.ggezRoutes()
	c.taskbarRoutes()
	c.miscRoutes()
}

// decode is a small helper for handlers taking a JSON body.
func decode[T any](r *http.Request) (T, error) {
	var v T
	err := httpx.Decode(r, &v)
	return v, err
}

func rawJSON(s string) json.RawMessage { return json.RawMessage(s) }
