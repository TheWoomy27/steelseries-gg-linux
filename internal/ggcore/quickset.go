package ggcore

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"ggl/internal/httpx"
	"ggl/internal/kwinfocus"
)

// QuickSet, GG's game profiles, as the GG core's loadouts service runs it:
//
//   - The Default profile ("Desktop") holds the configuration each app and
//     device uses outside games. Apps register theirs (Sonar posts its Game
//     preset to defaultConfig/add), and a device's current configuration
//     joins it before another profile takes the device over.
//   - A profile bound to a game is deployed while the game runs; when no
//     game runs, Default comes back. The Engine applies device
//     configurations, Sonar its preset (see the loadoutChanged hook).
//   - Apps cache their presets with the games each suits (Sonar's game
//     presets list Steam, Epic... ids). A game without a profile gets one
//     made from its Sonar preset when autoApplyQuicksetSonarPresets is on.
//   - Changing a configuration outside QuickSet (picking a Sonar preset, a
//     device configuration) changes the active profile's.
//
// Windows GG follows the foreground window. Wayland has no portable way to
// know it; on KDE a KWin script reports it (internal/kwinfocus), elsewhere
// a running game counts as in front.

// CachedPreset is an app's preset with the games it suits.
type CachedPreset struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	PresetMetadata string   `json:"preset_metadata"`
	PresetType     string   `json:"preset_type"`
	GameIDs        []string `json:"game_ids"`
}

type quicksetHooks struct {
	loadoutChanged   func(configs []LoadoutConfig) // a profile was deployed
	statusChanged    func()                        // profiles turned on or off
	loadoutsModified func()                        // profiles edited
}

// OnLoadoutChange registers fn for deployed profiles, with their configs.
func (c *Core) OnLoadoutChange(fn func(configs []LoadoutConfig)) {
	c.mu.Lock()
	c.quickset.loadoutChanged = fn
	c.mu.Unlock()
}

// OnQuicksetStatus registers fn for profiles being turned on or off.
func (c *Core) OnQuicksetStatus(fn func()) {
	c.mu.Lock()
	c.quickset.statusChanged = fn
	c.mu.Unlock()
}

// OnLoadoutsModified registers fn for profiles being added, renamed or
// changed.
func (c *Core) OnLoadoutsModified(fn func()) {
	c.mu.Lock()
	c.quickset.loadoutsModified = fn
	c.mu.Unlock()
}

func (c *Core) hooks() quicksetHooks {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.quickset
}

// QuicksetStatus says whether QuickSet is available and its profiles on.
func (c *Core) QuicksetStatus() (active, profilesEnabled bool) {
	return true, c.loadLoadouts().ProfilesEnabled
}

// Loadouts returns the profiles and the active one's id.
func (c *Core) Loadouts() ([]Loadout, string) {
	loadoutMu.Lock()
	defer loadoutMu.Unlock()
	st := c.loadLoadouts()
	return st.Loadouts, st.ActiveID
}

// GameImage is a scanned game's cover art, by unique identifier.
func (c *Core) GameImage(uid string) string {
	for _, g := range c.cachedGames() {
		if g.UniqueIdentifier == uid {
			return g.ImagePath
		}
	}
	return ""
}

// AddDefaultConfig gives the Default profile a configuration of a kind it
// does not have yet (an app's own setting outside games).
func (c *Core) AddDefaultConfig(cfg LoadoutConfig) {
	c.withLoadouts(func(st *loadoutState) (any, int) {
		d := findLoadout(st, defaultLoadoutID)
		if d == nil || hasConfig(d.Configs, cfg.ConfigType, cfg.ConfigMetadata) {
			return nil, 304
		}
		d.Configs = append(d.Configs, cfg)
		return nil, 200
	})
}

// SetActiveConfig puts a configuration in the active profile, replacing
// its configuration of that kind.
func (c *Core) SetActiveConfig(cfg LoadoutConfig) {
	c.withLoadouts(func(st *loadoutState) (any, int) {
		l := findLoadout(st, st.ActiveID)
		if l == nil {
			return nil, 404
		}
		for i, x := range l.Configs {
			if x.ConfigType == cfg.ConfigType && x.ConfigMetadata == cfg.ConfigMetadata {
				if x.ConfigID == cfg.ConfigID {
					return nil, 304
				}
				l.Configs[i] = cfg
				return nil, 200
			}
		}
		l.Configs = append(l.Configs, cfg)
		return nil, 200
	})
}

// ReplaceConfig points every profile using a configuration (one being
// deleted) at another.
func (c *Core) ReplaceConfig(typ, oldID, newID string) {
	c.withLoadouts(func(st *loadoutState) (any, int) {
		changed := false
		for i := range st.Loadouts {
			for j, x := range st.Loadouts[i].Configs {
				if x.ConfigType == typ && x.ConfigID == oldID {
					st.Loadouts[i].Configs[j].ConfigID = newID
					changed = true
				}
			}
		}
		if !changed {
			return nil, 304
		}
		return nil, 200
	})
}

func hasConfig(cfgs []LoadoutConfig, typ, metadata string) bool {
	return slices.ContainsFunc(cfgs, func(x LoadoutConfig) bool { return x.ConfigType == typ && x.ConfigMetadata == metadata })
}

// keepDefaultConfigs gives Default the current configuration of each
// device another profile is about to take over, so returning to Default
// restores it. Callers hold loadoutMu.
func (c *Core) keepDefaultConfigs(st *loadoutState, l *Loadout) {
	d := findLoadout(st, defaultLoadoutID)
	if d == nil || l.ID == defaultLoadoutID {
		return
	}
	for _, cfg := range l.Configs {
		if cfg.ConfigType != "engine" || hasConfig(d.Configs, "engine", cfg.ConfigMetadata) {
			continue
		}
		id, _ := strconv.Atoi(cfg.ConfigMetadata)
		if cur := c.eng.ActiveConfigID(id); cur != "" {
			d.Configs = append(d.Configs, LoadoutConfig{ConfigType: "engine", ConfigMetadata: cfg.ConfigMetadata, ConfigID: cur})
		}
	}
}

// profileType is how GG's analytics name a profile's kind.
func profileType(l *Loadout) string {
	switch {
	case l.ID == defaultLoadoutID:
		return "default"
	case slices.ContainsFunc(l.Bindings, func(b Binding) bool { return b.Type == "game" }):
		return "game"
	}
	return "custom"
}

// cachePresets keeps the presets an app sent (UpdatePresetsPayloadV1).
func cachePresets(st *loadoutState, payload map[string]any) {
	var p struct {
		AppName    string `json:"appName"`
		PresetList struct {
			Presets []CachedPreset `json:"presets"`
		} `json:"presetList"`
	}
	b, _ := json.Marshal(payload)
	if json.Unmarshal(b, &p) != nil || p.AppName == "" {
		return
	}
	if st.CachedPresets == nil {
		st.CachedPresets = map[string][]CachedPreset{}
	}
	st.CachedPresets[p.AppName] = p.PresetList.Presets
}

func (c *Core) setProfilesEnabled(on bool) {
	c.withLoadouts(func(st *loadoutState) (any, int) {
		st.ProfilesEnabled, st.ProfilesSet = on, true
		return nil, 200
	})
	c.Broadcast("EVENT_LOADOUTS_STATUS_CHANGE_V1", map[string]any{"active": true, "profiles_enabled": on})
	c.Broadcast("EVENT_APP_SETTING_CHANGED", map[string]any{"key": "quicksetProfilesEnabled", "value": on})
	if fn := c.hooks().statusChanged; fn != nil {
		go fn()
	}
}

// selectedOutsideQuickset follows a device configuration the user picked:
// the active profile uses it from now on.
func (c *Core) selectedOutsideQuickset(deviceID int, configID string) {
	if !c.loadLoadouts().ProfilesEnabled {
		return
	}
	c.SetActiveConfig(LoadoutConfig{ConfigType: "engine", ConfigMetadata: strconv.Itoa(deviceID), ConfigID: configID})
}

// loadoutForGame finds the profile bound to a game, or makes one from an
// app's preset for it.
func (c *Core) loadoutForGame(g ScannedGame) string {
	id := ""
	c.withLoadouts(func(st *loadoutState) (any, int) {
		for _, l := range st.Loadouts {
			for _, b := range l.Bindings {
				if b.Type == "game" && (b.Value == g.UniqueIdentifier || b.Value == g.ID) {
					id = l.ID
					return nil, 304
				}
			}
		}
		if c.Setting("autoApplyQuicksetSonarPresets") == false {
			return nil, 304
		}
		var preset *CachedPreset
		for _, p := range st.CachedPresets["sonar"] {
			for _, gid := range p.GameIDs {
				if strings.TrimSpace(gid) == g.UniqueIdentifier {
					preset = &p
					break
				}
			}
			if preset != nil {
				break
			}
		}
		if preset == nil {
			return nil, 304
		}
		// The new profile starts from Default, with the game's preset.
		var cfgs []LoadoutConfig
		if d := findLoadout(st, defaultLoadoutID); d != nil {
			for _, x := range d.Configs {
				if x.ConfigType != "sonar" {
					cfgs = append(cfgs, x)
				}
			}
		}
		cfgs = append(cfgs, LoadoutConfig{ConfigType: "sonar", ConfigID: preset.ID})
		l := Loadout{ID: httpx.RandomToken(8), Name: g.Name, CreationTime: time.Now().UTC(), Configs: cfgs,
			Bindings: []Binding{{Type: "game", Value: g.UniqueIdentifier,
				Data: BindingData{Name: g.Name, Directory: g.Directory, ExePath: g.ExePath, ImagePath: g.ImagePath}}}}
		st.Loadouts = append(st.Loadouts, l)
		id = l.ID
		slog.Info("quickset: profile made from a preset", "game", g.Name, "preset", preset.Name)
		return nil, 200
	})
	return id
}

// watchGames polls running games and switches profiles: the profile of
// the game in front, Default when no game is. Where the window in front is
// known (KDE, see FollowForeground) that is the game whose window it is;
// elsewhere a running game counts as in front. A profile deployed by hand
// stays until the game in front changes.
func (c *Core) watchGames(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	// Switching windows quickly (alt-tab through a few) deploys only the
	// profile of the one the user stays on.
	settle := time.NewTimer(time.Hour)
	settle.Stop()
	c.mu.Lock()
	fg := c.fg
	c.mu.Unlock()
	current := "" // the game whose profile is deployed
	for {
		select {
		case <-ctx.Done():
			return
		case <-fg.Changed():
			settle.Reset(500 * time.Millisecond)
			continue
		case <-settle.C:
		case <-t.C:
		}
		quickset := c.loadLoadouts().ProfilesEnabled && c.Setting("autoDeployLoadouts") != false
		if !quickset && !c.gameWatchersInterested() {
			current = ""
			c.setPlaying(nil)
			continue
		}
		ix := newGameIndex(c.cachedGames())
		running := ix.running()
		sort.Slice(running, func(i, j int) bool { return running[i].UniqueIdentifier < running[j].UniqueIdentifier })
		w, known := fg.Window()
		front, keep := frontGame(w, known, ix, running, current)
		if front != nil && !slices.ContainsFunc(running, func(g ScannedGame) bool { return g.ID == front.ID }) {
			running = append(running, *front)
		}
		c.setPlaying(running)
		if !quickset {
			current = ""
			continue
		}
		uid := ""
		if front != nil {
			uid = front.UniqueIdentifier
		}
		if keep || uid == current {
			continue
		}
		switch {
		case front != nil:
			if id := c.loadoutForGame(*front); id != "" {
				slog.Info("quickset: game in front", "game", front.Name)
				c.DeployLoadout(id, false)
			}
		default:
			slog.Info("quickset: no game in front, back to Default")
			c.DeployLoadout(defaultLoadoutID, false)
		}
		current = uid
	}
}

// frontGame is the game in front, nil for none, given the window in front
// when it is known; keep says to leave the deployed profile as it is.
func frontGame(w kwinfocus.Window, known bool, ix *gameIndex, running []ScannedGame, current string) (front *ScannedGame, keep bool) {
	if known {
		if g, ok := ix.windowGame(w); ok {
			return &g, false
		}
		// GG's own windows (its overlays over a game, editing the
		// game's profile) leave the profile alone.
		return nil, isGGWindow(w)
	}
	if i := slices.IndexFunc(running, func(g ScannedGame) bool { return g.UniqueIdentifier == current }); i >= 0 {
		return &running[i], false // still playing
	}
	if len(running) > 0 {
		return &running[0], false
	}
	return nil, false
}

// FollowForeground has QuickSet follow the window in front as fg reports
// it, instead of counting any running game as in front. Call it before
// Run.
func (c *Core) FollowForeground(fg *kwinfocus.Tracker) {
	c.mu.Lock()
	c.fg = fg
	c.mu.Unlock()
}

func (c *Core) quicksetRoutes() {
	s := c.srv
	addDefault := func(w http.ResponseWriter, r *http.Request) {
		cfg, err := decode[LoadoutConfig](r)
		if err != nil || cfg.ConfigType == "" {
			httpx.Error(w, 400, "bad config")
			return
		}
		c.AddDefaultConfig(cfg)
		httpx.OK(w)
	}
	s.Handle("POST /v1/loadouts/defaultConfig/add", addDefault)
	s.Handle("PUT /v1/loadouts/defaultConfig/add", addDefault)
	setActive := func(w http.ResponseWriter, r *http.Request) {
		cfg, err := decode[LoadoutConfig](r)
		if err != nil || cfg.ConfigType == "" {
			httpx.Error(w, 400, "bad config")
			return
		}
		c.SetActiveConfig(cfg)
		httpx.OK(w)
	}
	s.Handle("POST /v1/loadouts/defaultConfig/addOrUpdate", setActive)
	s.Handle("PUT /v1/loadouts/defaultConfig/addOrUpdate", setActive)
	removeDefault := func(w http.ResponseWriter, r *http.Request) {
		typ, meta, all := r.PathValue("configType"), r.PathValue("configMetadata"), r.PathValue("configMetadata") == ""
		c.withLoadouts(func(st *loadoutState) (any, int) {
			if d := findLoadout(st, defaultLoadoutID); d != nil {
				d.Configs = slices.DeleteFunc(d.Configs, func(x LoadoutConfig) bool {
					return x.ConfigType == typ && (all || x.ConfigMetadata == meta)
				})
			}
			return nil, 200
		})
		httpx.OK(w)
	}
	s.Handle("DELETE /v1/loadouts/defaultConfig/{configType}/{configMetadata}", removeDefault)
	s.Handle("DELETE /v1/loadouts/defaultConfig/{configType}", removeDefault)
	s.Handle("PUT /v1/loadouts/deleteConfig", c.deleteConfigHandler)
	s.Handle("POST /v1/loadouts/deleteConfig", c.deleteConfigHandler)
	s.Handle("GET /v1/loadouts/eligibleDevices", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"eligibleDevices": c.eng.EligibleDevices()})
	})
}

// deleteConfigHandler removes a deleted configuration from every profile.
func (c *Core) deleteConfigHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := decode[struct {
		ConfigID  string `json:"config_id"`
		ConfigID2 string `json:"configId"`
		ID        string `json:"id"`
	}](r)
	id := p.ConfigID + p.ConfigID2 + p.ID
	if id == "" {
		httpx.Error(w, 400, "no config id")
		return
	}
	c.withLoadouts(func(st *loadoutState) (any, int) {
		for i := range st.Loadouts {
			st.Loadouts[i].Configs = slices.DeleteFunc(st.Loadouts[i].Configs, func(x LoadoutConfig) bool { return x.ConfigID == id })
		}
		return nil, 200
	})
	httpx.OK(w)
}

// CachePresets keeps an app's presets with the games they suit.
func (c *Core) CachePresets(app string, presets []CachedPreset) {
	c.withLoadouts(func(st *loadoutState) (any, int) {
		if st.CachedPresets == nil {
			st.CachedPresets = map[string][]CachedPreset{}
		}
		st.CachedPresets[app] = presets
		return nil, 200
	})
}
