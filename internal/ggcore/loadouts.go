package ggcore

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"ggl/internal/httpx"
)

// Loadouts are GG's per-game profiles (QuickSet): a set of device/Sonar
// configs that is deployed when a bound game starts (or by keyboard
// shortcut). quickset.go switches them as games come and go.

type LoadoutConfig struct {
	ConfigType     string `json:"config_type"`
	ConfigMetadata string `json:"config_metadata"`
	ConfigID       string `json:"config_id"`
}

type Binding struct {
	Type  string      `json:"binding_type"`
	Value string      `json:"binding_value"`
	Data  BindingData `json:"binding_data"`
}

type BindingData struct {
	Name      string `json:"name"`
	Directory string `json:"directory"`
	ExePath   string `json:"exe_path"`
	ImagePath string `json:"image_path"`
}

type Loadout struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	CreationTime         time.Time       `json:"creationTime"`
	LastDeployTime       time.Time       `json:"lastDeployTime"`
	LastManualDeployTime time.Time       `json:"lastManualDeployTime"`
	Configs              []LoadoutConfig `json:"configs"`
	Bindings             []Binding       `json:"bindings"`
}

type ShortcutMapping struct {
	ShortcutIndex int    `json:"shortcut_index"`
	LoadoutID     string `json:"loadout_id"`
}

type loadoutState struct {
	Loadouts        []Loadout         `json:"loadouts"`
	ActiveID        string            `json:"activeId"`
	ProfilesEnabled bool              `json:"profilesEnabled"`
	ProfilesSet     bool              `json:"profilesSet"` // the user turned profiles on or off
	Shortcuts       []ShortcutMapping `json:"shortcuts"`
	Presets         map[string]any    `json:"presets"`
	// Apps' presets with the games they suit, by app (see quickset.go).
	CachedPresets map[string][]CachedPreset `json:"cachedPresets"`
}

const defaultLoadoutID = "default"

var loadoutMu sync.Mutex

func (c *Core) loadLoadouts() loadoutState {
	var st loadoutState
	c.db.Get("loadouts", &st)
	// Profiles are off until the user turns them on, as in GG on Windows
	// (earlier gg-linux builds stored them on).
	if !st.ProfilesSet {
		st.ProfilesEnabled = false
	}
	if len(st.Loadouts) == 0 {
		now := time.Now().UTC()
		st.Loadouts = []Loadout{{ID: defaultLoadoutID, Name: "Default", CreationTime: now, Configs: []LoadoutConfig{}, Bindings: []Binding{}}}
	}
	if st.ActiveID == "" {
		st.ActiveID = defaultLoadoutID
	}
	if st.Shortcuts == nil {
		st.Shortcuts = []ShortcutMapping{}
	}
	return st
}

func (c *Core) saveLoadouts(st loadoutState) { c.db.Set("loadouts", st) }

func findLoadout(st *loadoutState, id string) *Loadout {
	for i := range st.Loadouts {
		if st.Loadouts[i].ID == id {
			return &st.Loadouts[i]
		}
	}
	return nil
}

// withLoadouts runs fn under the loadout lock and persists the result.
func (c *Core) withLoadouts(fn func(st *loadoutState) (any, int)) (any, int) {
	loadoutMu.Lock()
	defer loadoutMu.Unlock()
	st := c.loadLoadouts()
	v, code := fn(&st)
	if code == 0 || code == 200 {
		c.saveLoadouts(st)
		ids := make([]string, 0, len(st.Loadouts))
		for _, l := range st.Loadouts {
			ids = append(ids, l.ID)
		}
		c.Broadcast("EVENT_LOADOUT_MODIFIED", map[string]any{"loadout_ids": ids})
		if fn := c.hooks().loadoutsModified; fn != nil {
			go fn()
		}
	}
	return v, code
}

// loadoutReader serves profiles without changing them.
func (c *Core) loadoutReader(fn func(st *loadoutState, r *http.Request) any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		loadoutMu.Lock()
		st := c.loadLoadouts()
		v := fn(&st, r)
		loadoutMu.Unlock()
		httpx.JSON(w, v)
	}
}

func (c *Core) loadoutHandler(fn func(st *loadoutState, r *http.Request) (any, int)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, code := c.withLoadouts(func(st *loadoutState) (any, int) { return fn(st, r) })
		if code != 0 && code != 200 {
			httpx.Error(w, code, "loadout not found")
			return
		}
		if v == nil {
			httpx.OK(w)
			return
		}
		httpx.JSON(w, v)
	}
}

// DeployLoadout switches the active loadout and asks the services that own
// each config to apply it.
func (c *Core) DeployLoadout(id string, manual bool) bool {
	ok := false
	var cfgs []LoadoutConfig
	var name, kind string
	c.withLoadouts(func(st *loadoutState) (any, int) {
		l := findLoadout(st, id)
		if l == nil {
			return nil, 404
		}
		c.keepDefaultConfigs(st, l)
		now := time.Now().UTC()
		l.LastDeployTime = now
		if manual {
			l.LastManualDeployTime = now
		}
		st.ActiveID = id
		cfgs, name, kind = append([]LoadoutConfig(nil), l.Configs...), l.Name, profileType(l)
		ok = true
		return nil, 200
	})
	if !ok {
		return false
	}
	for _, cfg := range cfgs {
		if cfg.ConfigType == "engine" || cfg.ConfigType == "prism" || cfg.ConfigType == "prismSync" || cfg.ConfigType == "" {
			c.eng.DeployConfigByID(cfg.ConfigID)
		}
	}
	if fn := c.hooks().loadoutChanged; fn != nil {
		fn(cfgs)
	}
	c.notifyLoadout(name, manual)
	c.Broadcast("EVENT_FOREGROUND_APP_OR_LOADOUT_CHANGE", map[string]any{
		"version":                 1,
		"loadoutId":               id,
		"configs":                 cfgs,
		"hasForegroundAppChange":  false,
		"hasLoadoutChange":        true,
		"foregroundAppChangeData": map[string]any{"path": "", "pid": 0, "hwnd": 0},
		"profileName":             name,
		"profileType":             kind,
		"saveToFlash":             false,
	})
	return true
}

func (c *Core) loadoutRoutes() {
	s := c.srv
	s.Handle("GET /v1/loadouts", c.loadoutReader(func(st *loadoutState, r *http.Request) any {
		return map[string]any{"loadouts": st.Loadouts}
	}))
	s.Handle("GET /v1/loadouts/active", c.loadoutReader(func(st *loadoutState, r *http.Request) any {
		return map[string]any{"activeLoadout": findLoadout(st, st.ActiveID)}
	}))
	s.Handle("GET /v1/loadouts/status", c.loadoutReader(func(st *loadoutState, r *http.Request) any {
		return map[string]any{"status": map[string]bool{"active": true, "profiles_enabled": st.ProfilesEnabled}}
	}))
	s.Handle("GET /v1/loadouts/devicesFamily", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"devicesFamily": c.eng.DeviceFamilies()})
	})
	s.Handle("GET /v1/loadouts/presets", c.loadoutReader(func(st *loadoutState, r *http.Request) any {
		if st.Presets == nil {
			return map[string]any{"version": 1, "presets": []any{}}
		}
		return st.Presets
	}))
	s.Handle("PUT /v1/loadouts/presets", c.loadoutHandler(func(st *loadoutState, r *http.Request) (any, int) {
		p, _ := decode[map[string]any](r)
		st.Presets = p
		cachePresets(st, p)
		return nil, 200
	}))
	s.Handle("GET /loadouts/{id}", c.loadoutReader(func(st *loadoutState, r *http.Request) any {
		return map[string]any{"loadout": findLoadout(st, r.PathValue("id"))}
	}))
	s.Handle("PUT /loadouts/{id}", c.loadoutHandler(func(st *loadoutState, r *http.Request) (any, int) {
		l := findLoadout(st, r.PathValue("id"))
		if l == nil {
			return nil, 404
		}
		p, _ := decode[struct {
			Name string `json:"name"`
		}](r)
		if p.Name != "" {
			l.Name = p.Name
		}
		return map[string]any{"loadouts": st.Loadouts}, 200
	}))
	s.Handle("DELETE /loadouts/{id}", c.loadoutHandler(func(st *loadoutState, r *http.Request) (any, int) {
		id := r.PathValue("id")
		if id == defaultLoadoutID {
			return nil, 400
		}
		out := st.Loadouts[:0]
		for _, l := range st.Loadouts {
			if l.ID != id {
				out = append(out, l)
			}
		}
		st.Loadouts = out
		if st.ActiveID == id {
			st.ActiveID = defaultLoadoutID
		}
		return nil, 200
	}))
	s.Handle("PUT /loadouts/{id}/configs", c.loadoutHandler(func(st *loadoutState, r *http.Request) (any, int) {
		l := findLoadout(st, r.PathValue("id"))
		if l == nil {
			return nil, 404
		}
		p, _ := decode[struct {
			Configs []LoadoutConfig `json:"configs"`
		}](r)
		l.Configs = p.Configs
		return map[string]any{"loadout": l}, 200
	}))
	s.Handle("PUT /loadouts/{id}/bindings", c.loadoutHandler(func(st *loadoutState, r *http.Request) (any, int) {
		l := findLoadout(st, r.PathValue("id"))
		if l == nil {
			return nil, 404
		}
		p, _ := decode[struct {
			Bindings []Binding `json:"bindings"`
		}](r)
		l.Bindings = p.Bindings
		return map[string]any{"loadout": l}, 200
	}))
	s.Handle("POST /loadouts/{id}/deploy", func(w http.ResponseWriter, r *http.Request) {
		if !c.DeployLoadout(r.PathValue("id"), true) {
			httpx.Error(w, 404, "loadout not found")
			return
		}
		httpx.OK(w)
	})
	newLoadout := func(st *loadoutState, name string, b *Binding) *Loadout {
		l := Loadout{ID: httpx.RandomToken(8), Name: name, CreationTime: time.Now().UTC(), Configs: []LoadoutConfig{}, Bindings: []Binding{}}
		if b != nil {
			l.Bindings = append(l.Bindings, *b)
		}
		st.Loadouts = append(st.Loadouts, l)
		return &st.Loadouts[len(st.Loadouts)-1]
	}
	s.Handle("POST /loadouts/withGame", c.loadoutHandler(func(st *loadoutState, r *http.Request) (any, int) {
		p, _ := decode[struct {
			UID  string `json:"uid"`
			Name string `json:"name"`
		}](r)
		l := newLoadout(st, p.Name, &Binding{Type: "game", Value: p.UID, Data: BindingData{Name: p.Name}})
		return map[string]any{"loadout": l}, 200
	}))
	s.Handle("POST /loadouts/withGame/deploy", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			UID  string `json:"uid"`
			Name string `json:"name"`
		}](r)
		var id string
		c.withLoadouts(func(st *loadoutState) (any, int) {
			id = newLoadout(st, p.Name, &Binding{Type: "game", Value: p.UID, Data: BindingData{Name: p.Name}}).ID
			return nil, 200
		})
		c.DeployLoadout(id, true)
		httpx.JSON(w, map[string]any{"id": id})
	})
	s.Handle("POST /loadouts/addCustom", c.loadoutHandler(func(st *loadoutState, r *http.Request) (any, int) {
		p, _ := decode[struct {
			Name    string `json:"name"`
			Binding string `json:"binding"`
		}](r)
		var b *Binding
		if p.Binding != "" {
			b = &Binding{Type: "custom", Value: p.Binding, Data: BindingData{Name: p.Name}}
		}
		return map[string]any{"loadout": newLoadout(st, p.Name, b)}, 200
	}))
	s.Handle("POST /loadouts/profiles/enable", func(w http.ResponseWriter, r *http.Request) {
		c.setProfilesEnabled(true)
		httpx.OK(w)
	})
	s.Handle("POST /loadouts/profiles/disable", func(w http.ResponseWriter, r *http.Request) {
		c.setProfilesEnabled(false)
		httpx.OK(w)
	})
	s.Handle("GET /loadouts/shortcuts", c.loadoutReader(func(st *loadoutState, r *http.Request) any {
		return map[string]any{"shortcuts": st.Shortcuts}
	}))
	s.Handle("PUT /loadouts/shortcuts/{idx}/{loadout}", c.loadoutHandler(func(st *loadoutState, r *http.Request) (any, int) {
		idx, _ := strconv.Atoi(r.PathValue("idx"))
		out := st.Shortcuts[:0]
		for _, m := range st.Shortcuts {
			if m.ShortcutIndex != idx {
				out = append(out, m)
			}
		}
		st.Shortcuts = append(out, ShortcutMapping{ShortcutIndex: idx, LoadoutID: r.PathValue("loadout")})
		return map[string]any{"shortcuts": st.Shortcuts}, 200
	}))
	s.Handle("DELETE /loadouts/shortcuts/{idx}", c.loadoutHandler(func(st *loadoutState, r *http.Request) (any, int) {
		idx, _ := strconv.Atoi(r.PathValue("idx"))
		out := st.Shortcuts[:0]
		for _, m := range st.Shortcuts {
			if m.ShortcutIndex != idx {
				out = append(out, m)
			}
		}
		st.Shortcuts = out
		return map[string]any{"shortcuts": st.Shortcuts}, 200
	}))
	s.Handle("GET /runningGame", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"runningGame": nil})
	})
}
