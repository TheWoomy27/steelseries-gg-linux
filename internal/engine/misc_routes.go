package engine

import (
	"encoding/json"
	"net/http"
	"sync"

	"ggl/internal/httpx"
)

func (e *Engine) macroRoutes() {
	s := e.srv
	macros := func() []map[string]any {
		return e.queryRows(`SELECT id, name, events, recording_options, is_new FROM macros WHERE user_id = ? AND is_deleted = 0`, e.userID)
	}
	s.Handle("GET /macros", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"macros": macros()}) })
	s.Handle("GET /macros/new", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"macros": e.queryRows(`SELECT id, name FROM macros WHERE user_id = ? AND is_new = 1 AND is_deleted = 0`, e.userID)})
	})
	s.Handle("POST /macros/clearNew", func(w http.ResponseWriter, r *http.Request) {
		e.db.Exec(`UPDATE macros SET is_new = 0 WHERE user_id = ?`, e.userID)
		httpx.OK(w)
	})
	s.Handle("POST /macro", func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		httpx.Decode(r, &p)
		id := newUUID()
		e.db.Exec(`INSERT INTO macros (id, name, events, user_id, recording_options, is_new) VALUES (?, ?, ?, ?, ?, 1)`,
			id, p["name"], jsonText(p["events"]), e.userID, jsonText(p["recording_options"]))
		p["id"] = id
		e.emit("macro_added", p)
		httpx.JSON(w, map[string]any{"macro": p})
	})
	s.Handle("PUT /macro/{mid}", func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		httpx.Decode(r, &p)
		e.db.Exec(`UPDATE macros SET name = ?, events = ?, recording_options = ? WHERE id = ?`,
			p["name"], jsonText(p["events"]), jsonText(p["recording_options"]), r.PathValue("mid"))
		httpx.JSON(w, map[string]any{"macro": p})
	})
	s.Handle("DELETE /macro/{mid}", func(w http.ResponseWriter, r *http.Request) {
		e.db.Exec(`UPDATE macros SET is_deleted = 1 WHERE id = ?`, r.PathValue("mid"))
		httpx.OK(w)
	})
	s.Handle("GET /macro/{mid}", func(w http.ResponseWriter, r *http.Request) {
		rows := e.queryRows(`SELECT id, name, events, recording_options FROM macros WHERE id = ?`, r.PathValue("mid"))
		if len(rows) == 0 {
			httpx.Error(w, 404, "no such macro")
			return
		}
		httpx.JSON(w, map[string]any{"macro": rows[0]})
	})
	s.Handle("POST /macro/validate", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"valid": true}) })

	s.Handle("GET /sseCommands", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"sseCommands": e.queryRows(`SELECT id, command_type, display_name, path, image_path, params FROM sse_commands WHERE user_id = ?`, e.userID)})
	})
	s.Handle("POST /sseCommand", func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		httpx.Decode(r, &p)
		id := newUUID()
		e.db.Exec(`INSERT INTO sse_commands (id, user_id, command_type, display_name, path, image_path, params) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, e.userID, p["command_type"], p["display_name"], p["path"], p["image_path"], jsonText(p["params"]))
		p["id"] = id
		httpx.JSON(w, map[string]any{"sseCommand": p})
	})
	s.Handle("GET /sseCommand/{sid}", func(w http.ResponseWriter, r *http.Request) {
		rows := e.queryRows(`SELECT id, command_type, display_name, path, image_path, params FROM sse_commands WHERE id = ?`, r.PathValue("sid"))
		if len(rows) == 0 {
			httpx.Error(w, 404, "no such command")
			return
		}
		httpx.JSON(w, map[string]any{"sseCommand": rows[0]})
	})
	s.Handle("PUT /sseCommand/{sid}", func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		httpx.Decode(r, &p)
		e.db.Exec(`UPDATE sse_commands SET command_type = ?, display_name = ?, path = ?, image_path = ?, params = ? WHERE id = ?`,
			p["command_type"], p["display_name"], p["path"], p["image_path"], jsonText(p["params"]), r.PathValue("sid"))
		httpx.JSON(w, map[string]any{"sseCommand": p})
	})
	s.Handle("DELETE /sseCommand/{sid}", func(w http.ResponseWriter, r *http.Request) {
		e.db.Exec(`DELETE FROM sse_commands WHERE id = ?`, r.PathValue("sid"))
		httpx.OK(w)
	})
}

// featureState stores simple on/off feature flags the UI toggles (Prism
// Sync, Image Sync, ...).
type featureState struct {
	mu sync.Mutex
	m  map[string]any
}

func (f *featureState) get(k string, def any) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.m[k]; ok {
		return v
	}
	return def
}

func (f *featureState) set(k string, v any) {
	f.mu.Lock()
	f.m[k] = v
	f.mu.Unlock()
}

func (e *Engine) miscRoutes() {
	s := e.srv
	feats := &featureState{m: map[string]any{}}
	toggle := func(path, key, event string, on bool) {
		s.Handle("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			feats.set(key, on)
			if event != "" {
				e.emit(event, map[string]any{"enabled": on})
			}
			httpx.OK(w)
		})
	}
	s.Handle("GET /prismSync", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"prismSync": map[string]any{"enabled": feats.get("prismSync", false)}, "enabled": feats.get("prismSync", false)})
	})
	toggle("/prismSync/enable", "prismSync", "prismSyncEnabledChanged", true)
	toggle("/prismSync/disable", "prismSync", "prismSyncEnabledChanged", false)
	for _, p := range []string{"/prismSync/updateBaseEffect", "/prismSync/updateTriggeredEffect", "/prismSync/updateIdleTimeAndEffect",
		"/prismSync/updatePrismSyncDevices", "/prismSync/leftHanded/enable", "/prismSync/leftHanded/disable",
		"/prismSync/inactiveMode/enable", "/prismSync/inactiveMode/disable", "/prismSync/updateTriggerModes",
		"/prismSyncV2/buffers", "/prismSyncV2/requestRedeployAllEngineConfigs", "/prismSyncV2/configChange/created",
		"/prismSyncV2/configChange/renamed", "/prismSyncV2/configChange/deleted"} {
		s.Handle("POST "+p, func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	}
	s.Handle("GET /prismSync/lightingTemplates", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"lightingTemplates": e.queryRows(`SELECT id, englishLabel, localeSubkey, settings FROM lighting_templates WHERE for_prism_sync = 1`)})
	})
	s.Handle("GET /prism/currentConfig", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"config": nil}) })
	s.Handle("GET /prism/connectedDevices", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"devices": e.devicesJSON(false)})
	})

	s.Handle("GET /imageSync", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"enabled": feats.get("imageSync", false)})
	})
	toggle("/imageSync/enable", "imageSync", "imageSyncEnabledChanged", true)
	toggle("/imageSync/disable", "imageSync", "imageSyncEnabledChanged", false)
	for _, p := range []string{"/imageSync/updateGif", "/imageSync/previewGif", "/imageSync/stopPreviewGif"} {
		s.Handle("POST "+p, func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	}
	s.Handle("GET /senseiRivalProDisplay", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"enabled": feats.get("shihan", false)})
	})
	toggle("/senseiRivalProDisplay/enable", "shihan", "", true)
	toggle("/senseiRivalProDisplay/disable", "shihan", "", false)

	// OS mouse settings (Windows pointer speed / acceleration). Linux
	// desktops own these; report neutral values.
	s.Handle("GET /systemSettings/mouse", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"get_mouse_settings": map[string]any{"cpi": 10, "accel": 0}})
	})
	s.Handle("POST /systemSettings/setMouse", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("POST /systemSettings/setDefaultMouseSettings", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })

	s.Handle("GET /spotify_auth_status", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"authorized": false}) })
	s.Handle("POST /spotify_auth_status", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("DELETE /spotify_auth_status", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })

	s.Handle("GET /libraryGames", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"games": []any{}}) })
	s.Handle("POST /libraryGames/add", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("POST /publicApiInternal/scan_and_add_featured_games/v1", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("GET /loadouts/presets", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"version": 1, "presets": []any{}})
	})
	s.Handle("GET /engineApps/supported", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"engineApps": []any{}}) })
	s.Handle("GET /applications", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"applications": e.queryRows(`SELECT * FROM applications`)})
	})
	s.Handle("POST /apps/search", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"apps": []any{}}) })
	s.Handle("GET /keyboardLayoutFallback/{id}", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"layout": nil}) })
	s.Handle("POST /keyboardLayoutFallback/set", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("POST /mouseTracking/startMouseTracking", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"id": newUUID()}) })
	s.Handle("POST /mouseTracking/stopMouseTracking/{tid}", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })

	// Global (per device, not per config) settings.
	s.Handle("GET /device/{id}/globalDeviceSettings", func(w http.ResponseWriter, r *http.Request) {
		var settings string
		e.db.QueryRow(`SELECT settings FROM global_device_settings WHERE device_id = ?`, pathInt(r, "id")).Scan(&settings)
		if settings == "" {
			if cat, err := e.catalogByID(pathInt(r, "id")); err == nil {
				settings = cat.GlobalSettings
			}
		}
		httpx.JSON(w, map[string]any{"globalDeviceSettings": jsonOrEmpty(settings)})
	})
	s.Handle("POST /device/{id}/globalDeviceSettings", func(w http.ResponseWriter, r *http.Request) {
		var p any
		httpx.Decode(r, &p)
		b, _ := json.Marshal(p)
		e.db.Exec(`INSERT OR REPLACE INTO global_device_settings (device_id, settings) VALUES (?, ?)`, pathInt(r, "id"), string(b))
		if l := e.liveByID(pathInt(r, "id")); l != nil {
			if m, ok := p.(map[string]any); ok {
				e.deploy(l, m, false, false)
			}
		}
		httpx.OK(w)
	})
	s.Handle("POST /device/{id}/globalDeviceSettings/liveDeploy", func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		httpx.Decode(r, &p)
		if l := e.liveByID(pathInt(r, "id")); l != nil {
			e.deploy(l, p, false, false)
		}
		httpx.OK(w)
	})
	s.Handle("DELETE /device/{id}/globalDeviceSettings/liveDeploy", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })

	s.Handle("POST /v1/device/{name}/flattenEQ", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("GET /v1/device/{name}/shouldFlattenEQ", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"shouldFlattenEQ": false})
	})

	// Sub-app actions (bind device buttons to Sonar/Moments actions).
	s.Handle("POST /v2/subAppActions", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("GET /v2/subAppActions/{id}/bindableActions", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"bindableActions": []any{}})
	})

	// Loadout-scoped generic device settings.
	s.Handle("GET /v2/device/config/loadout", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("device")
		httpx.JSON(w, map[string]any{"loadoutConfiguration": map[string]any{
			"device": name, "loadoutId": r.URL.Query().Get("loadoutId"), "configId": "",
			"settings": map[string]any{}, "statuses": e.genericStatuses(name),
		}})
	})
	s.Handle("POST /v1/device/settings", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })

	// "Audio lite": the Windows Engine's built-in audio EQ for headsets
	// when Sonar is off. On Linux, audio processing is Sonar (PipeWire).
	s.Handle("GET /v1/audioLite/devices", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"compatibleDeviceNames": []string{}})
	})
	s.Handle("GET /v1/audioLite/status", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"audioliteStatus": false}) })
	s.Handle("GET /v1/audioLite/deviceStatus", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"deviceName": "", "deviceDisplayName": "", "isDeviceActive": false, "isDeviceDefaulted": false, "isChatActive": false, "isChatDefaulted": false})
	})
	s.Handle("GET /v1/audioLite/deviceMetadata", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"mmDeviceIds": map[string]any{}})
	})
	for _, p := range []string{"/v1/audioLite/setupDevice", "/v1/audioLite/setupChat", "/v1/audioLite/loopback/start", "/v1/audioLite/loopback/stop",
		"/v1/audioSamplePlayer/play", "/v1/audioSamplePlayer/stop", "/backgroundInstall/{id}"} {
		s.Handle("POST "+p, func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	}
	s.Handle("GET /v1/audioLite/loopback/status", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"running": false}) })
}

// gameSenseRoutes serves the public GameSense API on the plain HTTP
// "address" from coreProps.json.
func (e *Engine) gameSenseRoutes() {
	g := e.gs
	gs := newGameSense(e)
	for _, p := range []string{"/game_metadata", "/register_game_event", "/bind_game_event", "/game_event", "/game_heartbeat",
		"/remove_game_event", "/remove_game", "/stop_game", "/multiple_game_events", "/load_golisp_handlers"} {
		path := p
		g.Handle("POST "+path, func(w http.ResponseWriter, r *http.Request) { gs.handle(w, r, path) })
	}
}
