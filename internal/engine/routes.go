package engine

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/steelseries/golisp"

	"ggl/internal/devicert"
	"ggl/internal/httpx"
)

func pathInt(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.PathValue(name))
	return n
}

// configPayload is what the client PUTs/POSTs for a configuration.
type configPayload struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Settings           any    `json:"settings"`
	UndeployedSettings any    `json:"undeployedSettings"`
	DeviceID           int    `json:"device_id"`
	Active             any    `json:"active"`
	ExtraProperties    any    `json:"extra_properties"`
	// Bindings are the configuration's bindings (absent: unchanged).
	Bindings json.RawMessage `json:"bindings"`
}

func jsonText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (e *Engine) routes() {
	s := e.srv
	s.Handle("GET /sock", e.hub.ServeHTTP)
	s.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("GET /assets/loadoutImages/{path...}", e.serveImages)

	// ---- devices
	s.Handle("GET /devices", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"devices": e.devicesJSON(true)})
	})
	s.Handle("GET /allDevices", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"devices": e.devicesJSON(true)})
	})
	s.Handle("GET /v1/connectedDevices", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"devices": e.loadoutDevicesJSON(true)})
	})
	s.Handle("GET /quickset/items", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"items": e.loadoutDevicesJSON(false)})
	})
	s.Handle("GET /device/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := pathInt(r, "id")
		cat, err := e.catalogByID(id)
		if err != nil {
			httpx.Error(w, 404, "no such device")
			return
		}
		httpx.JSON(w, map[string]any{"device": e.deviceJSON(cat, e.liveByID(id))})
	})
	s.Handle("POST /device/{id}/hide", func(w http.ResponseWriter, r *http.Request) {
		e.db.Exec(`UPDATE users_devices SET hidden = 1 WHERE user_id = ? AND device_id = ?`, e.userID, pathInt(r, "id"))
		httpx.OK(w)
	})
	s.Handle("POST /device/{id}/clearFirstPlugin", func(w http.ResponseWriter, r *http.Request) {
		e.db.Exec(`UPDATE users_devices SET first_plugin = 0 WHERE user_id = ? AND device_id = ?`, e.userID, pathInt(r, "id"))
		httpx.OK(w)
	})
	s.Handle("POST /device/{id}/optimizeFor/{child}", func(w http.ResponseWriter, r *http.Request) {
		e.db.Exec(`UPDATE users_devices SET optimized_for = ? WHERE user_id = ? AND device_id = ?`, pathInt(r, "child"), e.userID, pathInt(r, "id"))
		httpx.OK(w)
	})
	s.Handle("GET /device/{id}/isSyncAndSynced", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"isSyncDevice": false, "isSynced": true})
	})
	s.Handle("POST /device/{id}/displayedCounterfeitMessage", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("POST /device/{id}/updateCachedProperties", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("POST /device/{id}/lock", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("DELETE /device/{id}/lock", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("POST /device/{id}/disableAppDeploy", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("POST /device/{id}/enableAppDeploy", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("GET /device/{id}/configurations", func(w http.ResponseWriter, r *http.Request) {
		cfgs, err := e.configsForDevice(pathInt(r, "id"))
		if err != nil {
			httpx.Error(w, 500, err.Error())
			return
		}
		if cfgs == nil {
			cfgs = []*config{}
		}
		httpx.JSON(w, map[string]any{"configurations": cfgs})
	})
	// Device functions: (call-device-function ...) from the UI, e.g.
	// reading headset status for the device card.
	deviceFunc := func(w http.ResponseWriter, r *http.Request) {
		d := e.functionDecl(pathInt(r, "id"), uint32(pathInt(r, "pid")))
		if d == nil {
			httpx.Error(w, 404, "device not connected")
			return
		}
		name := r.PathValue("fn")
		v, err := d.Call(name)
		if err != nil {
			httpx.Error(w, 500, err.Error())
			return
		}
		httpx.JSON(w, map[string]any{"function_data": lispResult(v)})
	}
	s.Handle("POST /device/{id}/function/{fn}", deviceFunc)
	s.Handle("POST /device/{id}/function/{fn}/checkBootloader", deviceFunc)
	s.Handle("POST /device/{id}/physicalDevice/{pid}/function/{fn}/checkBootloader", deviceFunc)
	s.Handle("POST /device/{id}/deviceFunction/{fn}", deviceFunc)
	s.Handle("POST /device/{id}/physicalDevice/{pid}/function/{fn}", deviceFunc)
	s.Handle("GET /device/{id}/readFromDevice/{fn}", func(w http.ResponseWriter, r *http.Request) {
		l := e.liveByID(pathInt(r, "id"))
		if l == nil || l.primary() == nil {
			httpx.Error(w, 404, "device not connected")
			return
		}
		v, err := l.primary().ReadApi(r.PathValue("fn"), nil)
		if err != nil {
			httpx.Error(w, 500, err.Error())
			return
		}
		httpx.JSON(w, map[string]any{r.PathValue("fn"): v})
	})
	s.Handle("POST /device/command", e.handleDeviceWrite)
	s.Handle("POST /device/writeCommand", e.handleDeviceWrite)
	s.Handle("POST /device/writeCommandWithLightingPause", e.handleDeviceWrite)
	s.Handle("POST /device/writeCommandOnSecondaryPhysicalDevice", e.handleDeviceWrite)

	// Firmware updates are deliberately not wired up: flashing needs the
	// imported firmware images and an explicit opt-in (see README).
	for _, p := range []string{"PUT /device/{id}/firmware", "PUT /device/{id}/physicalDevice/{pid}/firmware"} {
		s.Handle(p, func(w http.ResponseWriter, r *http.Request) {
			httpx.Error(w, http.StatusNotImplemented, "firmware updates are disabled in gg-linux")
		})
	}

	// ---- configurations
	s.Handle("GET /configuration/{cid}", func(w http.ResponseWriter, r *http.Request) {
		c, err := e.configByID(r.PathValue("cid"))
		if err != nil {
			httpx.Error(w, 404, "no such configuration")
			return
		}
		httpx.JSON(w, map[string]any{"configuration": c})
	})
	s.Handle("POST /configuration", func(w http.ResponseWriter, r *http.Request) { e.handleCreateConfig(w, r, true) })
	s.Handle("POST /configuration/noDeploy", func(w http.ResponseWriter, r *http.Request) { e.handleCreateConfig(w, r, false) })
	s.Handle("PUT /configuration/{cid}", func(w http.ResponseWriter, r *http.Request) { e.handleSaveConfig(w, r, true) })
	s.Handle("PUT /configuration/{cid}/save", func(w http.ResponseWriter, r *http.Request) { e.handleSaveConfig(w, r, false) })
	s.Handle("POST /configuration/{cid}/deploy", func(w http.ResponseWriter, r *http.Request) { e.handleDeploy(w, r, false) })
	s.Handle("POST /configuration/{cid}/forceDeploy", func(w http.ResponseWriter, r *http.Request) { e.handleDeploy(w, r, true) })
	s.Handle("POST /configuration/{cid}/syncFromDevice", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("POST /configuration/{cid}/onboardSlot", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("PUT /configuration/{cid}/restore", func(w http.ResponseWriter, r *http.Request) {
		c, err := e.configByID(r.PathValue("cid"))
		if err != nil {
			httpx.Error(w, 404, "no such configuration")
			return
		}
		if cat, err := e.catalogByID(c.DeviceID); err == nil {
			c.Settings = cat.Settings
			e.saveConfig(c)
			e.db.Exec(`DELETE FROM bindings WHERE config_id = ?`, c.ID)
			e.withBindings(c)
		}
		httpx.JSON(w, map[string]any{"configuration": c})
	})
	s.Handle("DELETE /configuration/{cid}", func(w http.ResponseWriter, r *http.Request) {
		e.db.Exec(`UPDATE configurations SET is_deleted = 1, active = 0 WHERE id = ?`, r.PathValue("cid"))
		httpx.OK(w)
	})
	s.Handle("POST /configuration/validate", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"validation": map[string]any{"nameValidations": []string{}, "appValidations": []string{}}})
	})
	s.Handle("POST /configuration/liveDeploy", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			DeviceID int `json:"device_id"`
			Settings any `json:"settings"`
		}
		httpx.Decode(r, &p)
		l := e.liveByID(p.DeviceID)
		if l == nil {
			httpx.OK(w)
			return
		}
		settings := map[string]any{}
		json.Unmarshal([]byte(jsonText(p.Settings)), &settings)
		if err := e.deploy(l, settings, false, false); err != nil {
			slog.Debug("live deploy", "err", err)
		}
		httpx.OK(w)
	})
	s.Handle("POST /device/{id}/readconfigurations", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })

	// ---- application / misc
	s.Handle("GET /appInfo", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"application_info": map[string]any{
			"version": appVersion(), "os": "linux", "arch": runtime.GOARCH,
		}})
	})
	s.Handle("GET /application_setting", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"application_settings": e.appSettings()})
	})
	s.Handle("POST /application_setting", func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		httpx.Decode(r, &p)
		for k, v := range p {
			b, _ := json.Marshal(v)
			e.db.Exec(`DELETE FROM application_settings WHERE key = ?`, k)
			e.db.Exec(`INSERT INTO application_settings (key, value) VALUES (?, ?)`, k, string(b))
		}
		httpx.JSON(w, map[string]any{"application_settings": e.appSettings()})
	})
	// color_sequences rows are typed color_sequence or breathe_sequence.
	sequences := func(kind string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			httpx.JSON(w, map[string]any{"sequences": e.queryRows(`SELECT sequence_id, background_image, data, custom_gradient_data FROM color_sequences WHERE type = ?`, kind)})
		}
	}
	s.Handle("GET /color_sequences", sequences("color_sequence"))
	s.Handle("GET /breathe_sequences", sequences("breathe_sequence"))
	s.Handle("GET /oemInfo", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"isMsi": false}) })
	s.Handle("GET /isHpHost", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"isHpHost": false}) })
	s.Handle("GET /featureSplashInfos", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"feature_splash_infos": []any{}})
	})
	s.Handle("POST /featureSplashInfo", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("GET /cloud_settings", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"cloud_settings": []any{}}) })
	s.Handle("POST /cloud_settings", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("GET /cloudSyncInfo", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"cloud_sync_info": map[string]any{"last_sync_at": "", "last_sync_error_at": "", "last_sync_error_msg": ""}})
	})
	s.Handle("GET /resetAndRegisterUrls", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"urls": map[string]string{
			"signUp":        "https://steelseries.com/register",
			"resetPassword": "https://steelseries.com/password-reset",
			"myAccount":     "https://steelseries.com/account",
		}})
	})
	s.Handle("POST /cloudSync", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("POST /forceCloudSync", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("GET /keyboardRegion", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"region": keyboardRegion()})
	})
	s.Handle("POST /keyboardRegion/regionChanged", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("POST /analytics/track", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("GET /multimediadevices", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"multimediadevices": []any{}})
	})
	s.Handle("GET /v1/mmDevicesMetadata", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"mmDevicesMetadata": []any{}})
	})
	s.Handle("GET /v1/wirelessDeviceConnectionStatus", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"devices": e.wirelessStatuses()})
	})
	s.Handle("POST /v1/refreshWirelessDeviceConnectionStatus", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("GET /avneradevices", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{"devices": []any{}}) })
	s.Handle("GET /subApps", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"subApps": e.engineSubApps()})
	})
	s.Handle("POST /subApps/ready", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("POST /subApps/register", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })

	e.macroRoutes()
	e.miscRoutes()
	e.appRoutes()
	e.eqPresetRoutes()
}

func appVersion() string { return "123.0.0" }

func keyboardRegion() string {
	if v := golisp.Global.ValueOf(golisp.Intern("get-os-keyboard-region")); v != nil {
		if r, err := golisp.Eval(golisp.InternalMakeList(golisp.Intern("get-os-keyboard-region")), golisp.Global); err == nil {
			return golisp.StringValue(r)
		}
	}
	return "US"
}

func lispResult(v *golisp.Data) any {
	if v == nil {
		return nil
	}
	if golisp.StringP(v) {
		var out any
		if json.Unmarshal([]byte(golisp.StringValue(v)), &out) == nil {
			return out
		}
		return golisp.StringValue(v)
	}
	if golisp.IntegerP(v) {
		return golisp.IntegerValue(v)
	}
	if golisp.BooleanP(v) {
		return golisp.BooleanValue(v)
	}
	return golisp.LispWithFramesToJson(v)
}

// Engine application settings, with the defaults the client expects.
func (e *Engine) appSettings() map[string]any {
	out := map[string]any{
		"shouldAutoStart": true, "locale": "", "disableNotifications": false, "downloadBeforeNotify": false,
		"overrideWindowMouseSettings": false, "analyticsOptIn": false, "GGBetaOptIn": false,
		"generalBetaOptIn": false, "isBetaHighlighted": false,
	}
	rows, err := e.db.Query(`SELECT key, value FROM application_settings`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		var x any
		if json.Unmarshal([]byte(v), &x) != nil {
			x = v
		}
		out[k] = x
	}
	return out
}

func (e *Engine) queryRows(q string, args ...any) []map[string]any {
	out := []map[string]any{}
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if rows.Scan(ptrs...) != nil {
			continue
		}
		m := map[string]any{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out
}

// wirelessStatuses reports the wireless link of devices that have one
// (wireless_device_connection.WirelessDevConnStatus). mmDevices are Windows
// audio endpoint ids.
func (e *Engine) wirelessStatuses() []map[string]any {
	out := []map[string]any{}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, l := range e.live {
		l.mu.Lock()
		if l.linkKnown {
			out = append(out, map[string]any{"deviceId": l.dbID, "productId": l.cat.ProductID, "deviceName": l.cat.Name,
				"mmDevices": []string{}, "isConnected": l.linkUp})
		}
		l.mu.Unlock()
	}
	return out
}

// handleDeviceWrite runs a device command from the UI:
// {"id": N, "command": "{\"api\": {...}}"}. Commands naming read-only
// APIs perform the read (the spec's success handler reports the result as
// device events); the outcome is reported with a deviceLibRet event.
func (e *Engine) handleDeviceWrite(w http.ResponseWriter, r *http.Request) {
	var p struct {
		DeviceID int `json:"device_id"`
		ID       int `json:"id"`
		Command  any `json:"command"`
		Settings any `json:"settings"`
		Data     any `json:"data"`
	}
	if err := httpx.Decode(r, &p); err != nil {
		httpx.Error(w, 400, err.Error())
		return
	}
	id := p.DeviceID
	if id == 0 {
		id = p.ID
	}
	l := e.liveByID(id)
	if l == nil || l.primary() == nil {
		httpx.Error(w, 404, "device not connected")
		return
	}
	payload := p.Command
	if payload == nil {
		payload = p.Settings
	}
	if payload == nil {
		payload = p.Data
	}
	cmd := map[string]any{}
	json.Unmarshal([]byte(jsonText(payload)), &cmd)
	slog.Debug("device command", "device", l.cat.Name, "path", r.URL.Path, "command", truncateJSON(cmd))
	d := l.primary()
	ok := true
	for name, vals := range cmd {
		api := d.Api(name)
		var err error
		switch {
		case api == nil:
			continue
		case len(api.Write) == 0 && api.Read != nil:
			m, _ := vals.(map[string]any)
			_, err = d.ReadApi(name, m)
		default:
			_, err = e.writeSetting(d, name, vals, true)
		}
		if err != nil {
			ok = false
			slog.Debug("device command", "api", name, "err", err)
		}
	}
	e.emit("deviceLibRet", map[string]any{"id": id, "ssecmd": 0, "data": map[bool]int{true: 0, false: 1}[ok], "generic": true})
	httpx.OK(w)
}

func (e *Engine) handleCreateConfig(w http.ResponseWriter, r *http.Request, deploy bool) {
	var p configPayload
	if err := httpx.Decode(r, &p); err != nil {
		httpx.Error(w, 400, err.Error())
		return
	}
	c, err := e.createConfig(p.DeviceID, p.Name, jsonText(p.Settings), jsonText(p.UndeployedSettings), 0, 0)
	if err != nil {
		httpx.Error(w, 500, err.Error())
		return
	}
	if len(p.Bindings) > 0 && string(p.Bindings) != "null" {
		if err := e.setBindings(c.ID, p.Bindings); err != nil {
			slog.Warn("configuration bindings", "config", c.Name, "err", err)
		}
		e.withBindings(c)
	}
	if deploy {
		e.deployConfig(c, false)
		e.configSelected(c)
	}
	e.emit("reloadConfigs", map[string]any{"id": c.DeviceID})
	httpx.JSON(w, map[string]any{"configuration": c})
}

func (e *Engine) handleSaveConfig(w http.ResponseWriter, r *http.Request, deploy bool) {
	c, err := e.configByID(r.PathValue("cid"))
	if err != nil {
		httpx.Error(w, 404, "no such configuration")
		return
	}
	var p configPayload
	if err := httpx.Decode(r, &p); err != nil {
		httpx.Error(w, 400, err.Error())
		return
	}
	if p.Name != "" {
		c.Name = p.Name
	}
	if s := jsonText(p.Settings); s != "" {
		c.Settings = s
	}
	if s := jsonText(p.UndeployedSettings); s != "" {
		c.UndeployedSettings = s
	}
	if s := jsonText(p.ExtraProperties); s != "" {
		c.ExtraProperties = s
	}
	if err := e.saveConfig(c); err != nil {
		httpx.Error(w, 500, err.Error())
		return
	}
	if len(p.Bindings) > 0 && string(p.Bindings) != "null" {
		if err := e.setBindings(c.ID, p.Bindings); err != nil {
			slog.Warn("configuration bindings", "config", c.Name, "err", err)
		}
		e.withBindings(c)
	}
	if deploy {
		if err := e.deployConfig(c, false); err != nil {
			slog.Warn("deploy", "config", c.Name, "err", err)
		}
	}
	httpx.JSON(w, map[string]any{"configuration": c})
}

func (e *Engine) handleDeploy(w http.ResponseWriter, r *http.Request, force bool) {
	c, err := e.configByID(r.PathValue("cid"))
	if err != nil {
		httpx.Error(w, 404, "no such configuration")
		return
	}
	start := time.Now()
	if err := e.deployConfig(c, force); err != nil {
		httpx.Error(w, 500, err.Error())
		return
	}
	e.configSelected(c)
	slog.Info("configuration deployed", "config", c.Name, "ms", time.Since(start).Milliseconds())
	httpx.OK(w)
}

func truncateJSON(v any) string {
	b, _ := json.Marshal(v)
	if len(b) > 300 {
		return string(b[:300]) + "…"
	}
	return string(b)
}

// functionDecl is the spec a device function runs in: the connected
// device's (or the physical device pid's), or for a known device that is
// not connected its spec alone, which answers functions that need no I/O
// such as the firmware version GG ships for it.
func (e *Engine) functionDecl(id int, pid uint32) *devicert.Decl {
	if l := e.liveByID(id); l != nil {
		l.mu.Lock()
		d := l.physical[pid]
		l.mu.Unlock()
		if d != nil {
			return d
		}
		if d := l.primary(); d != nil {
			return d
		}
	}
	cat, err := e.catalogByID(id)
	if err != nil {
		return nil
	}
	pids := []uint32{pid, uint32(cat.ProductID)}
	if phys, err := e.physicalDevices(uint32(cat.ProductID)); err == nil {
		for _, p := range phys {
			pids = append(pids, p.ProductID)
		}
	}
	for _, p := range pids {
		if p != 0 && e.rt.KnownDevice(p) {
			if d, err := e.rt.Device(p); err == nil {
				return d
			}
		}
	}
	return nil
}
