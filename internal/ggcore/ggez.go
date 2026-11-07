package ggcore

import (
	"net/http"
	"sync"

	"ggl/internal/httpx"
)

// Endpoints that SteelSeriesGGEZ.exe (the .NET half of the Windows GG
// core) serves: the app key store, pinnable tabs, the apps tray, the
// taskbar menu and universal triggers.

type appKey struct {
	App   string `json:"app"`
	Key   string `json:"key"`
	Value any    `json:"value"`
}

// TrayMenu is the taskbar menu the client describes via /taskbar/setMenu.
type TrayMenu struct {
	mu    sync.Mutex
	items []any
	onSet func(items []any)
}

func (t *TrayMenu) Items() []any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.items
}

// OnChange registers the tray implementation.
func (t *TrayMenu) OnChange(fn func(items []any)) {
	t.mu.Lock()
	t.onSet = fn
	t.mu.Unlock()
}

// pinnableTab is one entry of the side navigation (pinned apps show in the
// sidebar, the rest live in the apps tray).
type pinnableTab struct {
	Name     string `json:"name"`
	IsPinned bool   `json:"isPinned"`
}

var defaultTabs = []pinnableTab{
	{"engine", true}, {"sonar", true}, {"moments", true},
	{"giveaways", false}, {"news", false}, {"aimtools", false},
}

func (c *Core) pinnableTabs() []pinnableTab {
	var tabs []pinnableTab
	if !c.db.Get("pinnableTabs", &tabs) || len(tabs) == 0 {
		tabs = append([]pinnableTab{}, defaultTabs...)
	}
	return tabs
}

func (c *Core) ggezRoutes() {
	s := c.srv
	s.Handle("GET /v1/appKeyStore", func(w http.ResponseWriter, r *http.Request) {
		var keys []appKey
		c.db.Get("appKeys", &keys)
		app, key := r.URL.Query().Get("app"), r.URL.Query().Get("key")
		out := []appKey{}
		for _, k := range keys {
			if (app == "" || k.App == app) && (key == "" || k.Key == key) {
				out = append(out, k)
			}
		}
		if key != "" && len(out) == 1 {
			httpx.JSON(w, map[string]any{"appKey": out[0], "value": out[0].Value})
			return
		}
		httpx.JSON(w, map[string]any{"appKeys": out})
	})
	s.Handle("PUT /v1/appKeyStore", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[appKey](r)
		var keys []appKey
		c.db.Get("appKeys", &keys)
		found := false
		for i := range keys {
			if keys[i].App == p.App && keys[i].Key == p.Key {
				keys[i].Value, found = p.Value, true
			}
		}
		if !found {
			keys = append(keys, p)
		}
		c.db.Set("appKeys", keys)
		c.Broadcast("EVENT_APP_KEYSTORE_CHANGED", p)
		httpx.OK(w)
	})

	s.Handle("GET /pinnableTab", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, c.pinnableTabs())
	})
	s.Handle("POST /pinnableTab", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[pinnableTab](r)
		tabs := c.pinnableTabs()
		for i := range tabs {
			if tabs[i].Name == p.Name {
				tabs[i].IsPinned = p.IsPinned
			}
		}
		c.db.Set("pinnableTabs", tabs)
		c.Broadcast("EVENT_PINNABLE_TAB_CHANGED", p)
		httpx.JSON(w, tabs)
	})

	s.Handle("GET /appsTray", func(w http.ResponseWriter, r *http.Request) {
		enabled := true
		c.db.Get("appsTrayEnabled", &enabled)
		httpx.JSON(w, map[string]any{"enabled": enabled})
	})
	s.Handle("POST /appsTray", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			Enabled bool `json:"enabled"`
		}](r)
		c.db.Set("appsTrayEnabled", p.Enabled)
		c.Broadcast("EVENT_APPS_TRAY_ENABLE_CHANGED", map[string]any{"enabled": p.Enabled})
		httpx.OK(w)
	})

	s.Handle("POST /taskbar/setMenu", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			Items []any `json:"items"`
			Menu  []any `json:"menu"`
		}](r)
		items := p.Items
		if items == nil {
			items = p.Menu
		}
		c.tray.mu.Lock()
		c.tray.items = items
		fn := c.tray.onSet
		c.tray.mu.Unlock()
		if fn != nil {
			fn(items)
		}
		httpx.OK(w)
	})
	s.Handle("POST /taskbar/clearMenu", func(w http.ResponseWriter, r *http.Request) {
		c.tray.mu.Lock()
		c.tray.items = nil
		fn := c.tray.onSet
		c.tray.mu.Unlock()
		if fn != nil {
			fn(nil)
		}
		httpx.OK(w)
	})
	for _, p := range []string{"/taskbar/icon", "/taskbar/notification"} {
		s.Handle("POST "+p, func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	}

	s.Handle("GET /universalTriggers", func(w http.ResponseWriter, r *http.Request) {
		var t []any
		c.db.Get("universalTriggers", &t)
		if t == nil {
			t = []any{}
		}
		httpx.JSON(w, map[string]any{"universalTriggers": t})
	})
	s.Handle("POST /universalTriggers", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			UniversalTriggers []any `json:"universalTriggers"`
		}](r)
		c.db.Set("universalTriggers", p.UniversalTriggers)
		httpx.OK(w)
	})
	s.Handle("POST /windowInformation", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"windows": []any{}})
	})
}
