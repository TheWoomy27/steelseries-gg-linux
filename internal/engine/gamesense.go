package engine

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"ggl/internal/devicert"
	"ggl/internal/gamesense"
	"ggl/internal/httpx"
)

// gameSense serves the public GameSense SDK endpoints games call. Their
// lighting handlers run in gamesense.State, which Prism draws over its
// own lighting; screen and tactile handlers are recorded but not run.
type gameSense struct {
	e     *Engine
	state *gamesense.State
	mu    sync.Mutex // the stored games and user handlers
}

func newGameSense(e *Engine) *gameSense {
	g := &gameSense{e: e, state: gamesense.New()}
	var disabled []string
	e.state.Get("gameSenseDisabled", &disabled)
	for _, name := range disabled {
		g.state.SetEnabled(name, false)
	}
	g.load()
	g.state.OnActive = func(game string, active bool) {
		slog.Info("gamesense", "game", game, "active", active)
		e.emit("gamesenseGameInitializationChanged", map[string]any{"game": game, "initialized": active})
	}
	return g
}

// GameSense is the state of the games lighting devices through
// GameSense.
func (e *Engine) GameSense() *gamesense.State { return e.gameSense.state }

// gsName is what the SDK allows in game and event names.
var gsName = regexp.MustCompile(`^[A-Z0-9_-]+$`)

// gsError answers with the SDK's error shape.
func gsError(w http.ResponseWriter, code int, msg string) {
	httpx.JSONStatus(w, http.StatusBadRequest, map[string]any{"error": msg, "code": code})
}

func (g *gameSense) handle(w http.ResponseWriter, r *http.Request, path string) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	var p struct {
		Game          string            `json:"game"`
		Event         string            `json:"event"`
		DisplayName   string            `json:"game_display_name"`
		Developer     string            `json:"developer"`
		Deinit        float64           `json:"deinitialize_timer_length_ms"`
		Min           *float64          `json:"min_value"`
		Max           *float64          `json:"max_value"`
		IconID        int               `json:"icon_id"`
		ValueOptional bool              `json:"value_optional"`
		Handlers      []json.RawMessage `json:"handlers"`
		Data          json.RawMessage   `json:"data"`
		Events        []struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad json")
		return
	}
	game := strings.ToUpper(p.Game)
	if game == "" {
		gsError(w, 1, "Game string not specified")
		return
	}
	if !gsName.MatchString(game) {
		gsError(w, 2, "Game string contains disallowed characters")
		return
	}
	needEvent := path == "/register_game_event" || path == "/bind_game_event" || path == "/game_event" || path == "/remove_game_event"
	event := strings.ToUpper(p.Event)
	if needEvent && event == "" {
		gsError(w, 0, "Game or event string not specified")
		return
	}
	if needEvent && !gsName.MatchString(event) {
		gsError(w, 3, "Event string contains disallowed characters")
		return
	}
	info := gamesense.EventInfo{Max: 100, ValueOptional: p.ValueOptional, IconID: p.IconID}
	if p.Min != nil {
		info.Min = *p.Min
	}
	if p.Max != nil {
		info.Max = *p.Max
	}
	s := g.state
	switch path {
	case "/game_metadata":
		s.Metadata(game, p.DisplayName, p.Developer, int(p.Deinit))
	case "/register_game_event":
		s.Register(game, event, info)
	case "/bind_game_event":
		if len(p.Handlers) == 0 {
			gsError(w, 6, "One or more handlers must be specified for binding")
			return
		}
		s.Bind(game, event, info, p.Handlers)
	case "/remove_game_event":
		s.RemoveEvent(game, event)
	case "/remove_game":
		s.RemoveGame(game)
	case "/game_event":
		if len(p.Data) == 0 {
			gsError(w, 4, "GameEvent data member is empty")
			return
		}
		s.Event(game, event, p.Data)
	case "/multiple_game_events":
		for _, ev := range p.Events {
			s.Event(game, strings.ToUpper(ev.Event), ev.Data)
		}
	case "/game_heartbeat":
		s.Heartbeat(game)
	case "/stop_game":
		s.Stop(game)
	}
	switch path {
	case "/game_metadata", "/register_game_event", "/bind_game_event", "/remove_game", "/remove_game_event":
		g.remember(path, game, event, body)
		g.e.emit("EVENT_GAMESENSE_NEW_DATA", map[string]any{"game": game})
	}
	httpx.JSON(w, map[string]any{"game": game})
}

// setEnabled is GG's switch for a game's GameSense lighting.
func (g *gameSense) setEnabled(game string, on bool) {
	game = strings.ToUpper(game)
	g.state.SetEnabled(game, on)
	var disabled []string
	g.e.state.Get("gameSenseDisabled", &disabled)
	out := disabled[:0]
	for _, n := range disabled {
		if n != game {
			out = append(out, n)
		}
	}
	if !on {
		out = append(out, game)
	}
	g.e.state.Set("gameSenseDisabled", out)
	g.e.emit("EVENT_GAMESENSE_GAME_ENABLED_CHANGED", map[string]any{"game": game, "enabled": on})
}

// Games and their handlers are kept across restarts, as the Windows
// Engine keeps them, so GG's GameSense page lists a game that is not
// running: its game_metadata, and each event's latest
// register_game_event or bind_game_event.
type storedGame struct {
	Metadata json.RawMessage            `json:"metadata,omitempty"`
	Events   map[string]json.RawMessage `json:"events,omitempty"`
}

// userKey names a user handler: game, event and device product.
func userKey(game, event string, pid uint32) string {
	return game + "/" + event + "/" + strconv.FormatUint(uint64(pid), 10)
}

func (g *gameSense) load() {
	var games map[string]storedGame
	g.e.state.Get("gameSenseGames", &games)
	for name, sg := range games {
		var m struct {
			DisplayName string  `json:"game_display_name"`
			Developer   string  `json:"developer"`
			Deinit      float64 `json:"deinitialize_timer_length_ms"`
		}
		if json.Unmarshal(sg.Metadata, &m) == nil {
			g.state.Metadata(name, m.DisplayName, m.Developer, int(m.Deinit))
		}
		for ev, body := range sg.Events {
			info, handlers := eventRequest(body)
			if len(handlers) > 0 {
				g.state.Bind(name, ev, info, handlers)
			} else {
				g.state.Register(name, ev, info)
			}
		}
	}
	var user map[string]json.RawMessage
	g.e.state.Get("gameSenseUser", &user)
	for k, raw := range user {
		parts := strings.Split(k, "/")
		if len(parts) != 3 {
			continue
		}
		if pid, err := strconv.ParseUint(parts[2], 10, 32); err == nil {
			g.state.SetUserHandler(parts[0], parts[1], uint32(pid), raw)
		}
	}
	var screens map[string]json.RawMessage
	g.e.state.Get("gameSenseUserScreens", &screens)
	for k, raw := range screens {
		parts := strings.Split(k, "/")
		if len(parts) != 3 {
			continue
		}
		if pid, err := strconv.ParseUint(parts[2], 10, 32); err == nil {
			g.state.SetUserScreen(parts[0], parts[1], uint32(pid), raw)
		}
	}
}

// eventRequest reads a register or bind request.
func eventRequest(body json.RawMessage) (gamesense.EventInfo, []json.RawMessage) {
	var p struct {
		Min           *float64          `json:"min_value"`
		Max           *float64          `json:"max_value"`
		IconID        int               `json:"icon_id"`
		ValueOptional bool              `json:"value_optional"`
		Handlers      []json.RawMessage `json:"handlers"`
	}
	json.Unmarshal(body, &p)
	info := gamesense.EventInfo{Max: 100, ValueOptional: p.ValueOptional, IconID: p.IconID}
	if p.Min != nil {
		info.Min = *p.Min
	}
	if p.Max != nil {
		info.Max = *p.Max
	}
	return info, p.Handlers
}

// remember updates the stored games after a request.
func (g *gameSense) remember(path, game, event string, body json.RawMessage) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var games map[string]storedGame
	g.e.state.Get("gameSenseGames", &games)
	if games == nil {
		games = map[string]storedGame{}
	}
	sg := games[game]
	if sg.Events == nil {
		sg.Events = map[string]json.RawMessage{}
	}
	switch path {
	case "/game_metadata":
		sg.Metadata = body
	case "/register_game_event":
		// Registering again keeps the handlers bound before.
		if _, h := eventRequest(sg.Events[event]); len(h) > 0 {
			var m map[string]any
			json.Unmarshal(body, &m)
			m["handlers"] = h
			body, _ = json.Marshal(m)
		}
		sg.Events[event] = body
	case "/bind_game_event":
		sg.Events[event] = body
	case "/remove_game_event":
		delete(sg.Events, event)
	case "/remove_game":
		delete(games, game)
		g.e.state.Set("gameSenseGames", games)
		var user map[string]json.RawMessage
		g.e.state.Get("gameSenseUser", &user)
		for k := range user {
			if strings.HasPrefix(k, game+"/") {
				delete(user, k)
			}
		}
		g.e.state.Set("gameSenseUser", user)
		return
	}
	games[game] = sg
	g.e.state.Set("gameSenseGames", games)
}

// GameSense pages: GG's Engine apps list the registered games, and their
// configuration shows each lit device's events with the game's handler
// for it and the user's own.

// GameIntegrationGames is GET /game_integration_games.
func (e *Engine) GameIntegrationGames() any {
	out := []map[string]any{}
	for i, g := range e.gameSense.state.Games() {
		name := g.DisplayName
		if name == "" {
			name = g.Name
		}
		out = append(out, map[string]any{"id": i + 1, "game": g.Name, "game_display_name": name, "developer": g.Developer,
			"launchable_engine_app_id": "", "local_app_logo_path": "", "local_app_tile_path": "", "game_enabled": g.Enabled})
	}
	return out
}

// GameIntegrationDevices is GET /game_integration_devices: the devices
// GameSense lights (screens and vibration are not run on Linux, so they
// are left out).
func (e *Engine) GameIntegrationDevices() any {
	out := []map[string]any{}
	details := e.gameSense.state.Details()
	// The devices GameSense lights (Prism's), and those with a screen.
	type gsDev struct {
		id       int
		pid      uint32
		typ      int
		name, fn string
		info     *devicert.GameSenseInfo
	}
	var devs []gsDev
	seen := map[int]bool{}
	for _, pd := range e.PrismSyncDevices() {
		if pd.GameSense != nil {
			devs = append(devs, gsDev{pd.ID, pd.ProductID, pd.Type, pd.Name, pd.FullName, pd.GameSense})
			seen[pd.ID] = true
		}
	}
	for _, od := range e.oledDevices() {
		if !seen[od.id] {
			if l := e.liveByID(od.id); l != nil {
				devs = append(devs, gsDev{od.id, od.decl.ID, l.cat.Type, l.cat.Name, l.cat.FullName, e.gameSenseInfo(od.decl)})
			}
		}
	}
	for _, d := range devs {
		info := d.info
		var types []string
		base, zonesCount := "", 0
		screened := false
		for _, t := range info.Types {
			if t == "tactile" {
				continue
			}
			types = append(types, t)
			if t == "rgb-per-key-zones" || t == "rgb-composite-zones" || zonedType(t) {
				base = t
			}
			screened = screened || t == "screened"
		}
		if base == "" && !screened {
			continue
		}
		for z, codes := range info.Zones {
			if z != "all" && len(codes) == 1 {
				zonesCount++
			}
		}
		if base != "rgb-per-key-zones" {
			zonesCount = len(info.Zones["all"])
		}
		dev := &gamesense.Device{ProductID: d.pid, Types: info.Types, Zones: info.Zones}
		screenZones, screenSizes, screenLines := map[string]any{}, map[string]any{}, map[string]any{}
		for z, n := range info.ScreenZones {
			screenZones[z] = []int{n}
		}
		for z, wh := range info.ScreenSizes {
			screenSizes[z] = []int{wh[0], wh[1]}
			dev.ScreenW, dev.ScreenH = wh[0], wh[1]
		}
		for z, n := range info.ScreenLines {
			screenLines[z] = n
		}
		var games []map[string]any
		for _, gd := range details {
			var events []map[string]any
			for _, ev := range gd.Events {
				def := json.RawMessage("{}")
				bySpecifier := map[string][]string{}
				for _, h := range ev.Handlers {
					if h.Zone != "" && h.DeviceType != "" {
						bySpecifier[h.DeviceType] = append(bySpecifier[h.DeviceType], h.Zone)
					}
					if string(def) == "{}" && h.Applies(dev) {
						def = h.Raw
					}
				}
				// "handler" is what the event shows on the device: the
				// user's handler, else the game's.
				user := def
				if h := ev.User[d.pid]; h != nil {
					user = h.Raw
				}
				screen := json.RawMessage("{}")
				for _, h := range ev.Screens {
					if h.Applies(dev) {
						screen = h.Raw
						break
					}
				}
				userScreen := screen
				if h := ev.UserScreens[d.pid]; h != nil {
					userScreen = h.Raw
				}
				events = append(events, map[string]any{"event": ev.Name, "min_value": ev.Info.Min, "max_value": ev.Info.Max,
					"icon_id": ev.Info.IconID, "value_optional": ev.Info.ValueOptional, "zones_by_specifier": bySpecifier,
					"illumination_handler": map[string]string{"handler": string(user), "default_handler": string(def)},
					"vibration_handler":    map[string]string{"handler": "{}", "default_handler": "{}"},
					"screen_handler":       map[string]string{"handler": string(userScreen), "default_handler": string(screen)}})
			}
			name := gd.DisplayName
			if name == "" {
				name = gd.Name
			}
			games = append(games, map[string]any{"game": gd.Name, "game_display_name": name, "local_app_logo_path": "",
				"templates": []any{}, "zone_to_custom_zone_keys_lookup": map[string]any{}, "game_enabled": gd.Enabled, "events": events})
		}
		if games == nil {
			games = []map[string]any{}
		}
		out = append(out, map[string]any{"id": d.id, "main_device_product_id": d.pid, "name": d.name, "full_name": d.fn,
			"type": d.typ, "zone_to_custom_zone_keys_lookup": info.Zones, "screen_dimensions": screenSizes,
			"tactile_zone_to_custom_zone_keys_lookup": map[string]any{}, "screen_zone_to_custom_zone_keys_lookup": screenZones,
			"screen_zone_to_max_lines_lookup": screenLines, "device_file_base_lighting_type": base,
			"number_of_lighting_zones": zonesCount, "number_of_tactile_zones": 0, "number_of_screen_zones": len(screenZones),
			"device_file_device_types": types, "game_event_handlers": games})
	}
	return out
}

func zonedType(t string) bool {
	return strings.HasPrefix(t, "rgb-") && strings.HasSuffix(t, "-zone") && t != "rgb-per-key-zone"
}

// ToggleGame is POST /game_integration_toggle.
func (e *Engine) ToggleGame(game string, on bool) { e.gameSense.setEnabled(game, on) }

// SetUserHandlers is POST /set_user_handlers: the user's handlers for a
// game's events on one device.
func (e *Engine) SetUserHandlers(body []byte) error {
	var p struct {
		Game      string `json:"game"`
		ProductID uint32 `json:"product_id"`
		Events    []struct {
			Event        string `json:"event"`
			Illumination struct {
				Handler string `json:"handler"`
			} `json:"illumination_handler"`
			Screen *struct {
				Handler        string `json:"handler"`
				DefaultHandler string `json:"default_handler"`
			} `json:"screen_handler"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return err
	}
	g := e.gameSense
	game := strings.ToUpper(p.Game)
	g.mu.Lock()
	defer g.mu.Unlock()
	var user, screens map[string]json.RawMessage
	e.state.Get("gameSenseUser", &user)
	if user == nil {
		user = map[string]json.RawMessage{}
	}
	e.state.Get("gameSenseUserScreens", &screens)
	if screens == nil {
		screens = map[string]json.RawMessage{}
	}
	for _, ev := range p.Events {
		if sc := ev.Screen; sc != nil {
			// The page sends the game's handler back unchanged for the
			// events the user left alone.
			raw := json.RawMessage(sc.Handler)
			key := userKey(game, strings.ToUpper(ev.Event), p.ProductID)
			if !json.Valid(raw) || sameJSON(raw, json.RawMessage(sc.DefaultHandler)) || string(raw) == "{}" {
				raw = json.RawMessage("{}")
				delete(screens, key)
			} else {
				screens[key] = raw
			}
			g.state.SetUserScreen(game, strings.ToUpper(ev.Event), p.ProductID, raw)
		}
		raw := json.RawMessage(ev.Illumination.Handler)
		if !json.Valid(raw) {
			raw = json.RawMessage("{}")
		}
		event := strings.ToUpper(ev.Event)
		g.state.SetUserHandler(game, event, p.ProductID, raw)
		if string(raw) == "{}" {
			delete(user, userKey(game, event, p.ProductID))
		} else {
			user[userKey(game, event, p.ProductID)] = raw
		}
	}
	slog.Info("gamesense: user handlers", "game", game, "device", p.ProductID, "events", len(p.Events))
	if err := e.state.Set("gameSenseUserScreens", screens); err != nil {
		return err
	}
	return e.state.Set("gameSenseUser", user)
}

// sameJSON reports whether two JSON texts hold the same value.
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
