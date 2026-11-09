package engine

import (
	"encoding/json"
	"net/http"

	"ggl/internal/httpx"
)

// Auto-flatten EQ: while Sonar handles EQ, a headset's own 2.4 GHz EQ
// should be flat, or the two stack. GG asks the Engine whether a device's
// EQ conflicts with Sonar (Sonar on, GG's Auto-Flatten EQ setting on, and
// the active configuration's parametric_eq not flat), shows a banner, and
// asks the Engine to flatten it: every band's gain goes to 0, in the
// configuration and on the device. gg-linux only flattens when asked.

const peqSetting = "parametric_eq"

// SetAutoFlatten tells the Engine whether Sonar handles EQ with GG's
// Auto-Flatten EQ setting on.
func (e *Engine) SetAutoFlatten(fn func() bool) {
	e.mu.Lock()
	e.autoFlatten = fn
	e.mu.Unlock()
}

// eqIsFlat reports whether a parametric_eq setting leaves the sound alone.
func eqIsFlat(peq map[string]any) bool {
	data, _ := peq["eq_data"].(map[string]any)
	for _, b := range data {
		band, _ := b.(map[string]any)
		enabled, _ := number(band["enabled"])
		gain, _ := band["gain"].(float64)
		if enabled != 0 && gain != 0 {
			return false
		}
	}
	return true
}

// flatEQ is a parametric_eq setting with every band's gain at 0.
func flatEQ(peq map[string]any) map[string]any {
	var out map[string]any
	b, _ := json.Marshal(peq)
	json.Unmarshal(b, &out)
	data, _ := out["eq_data"].(map[string]any)
	for _, b := range data {
		if band, ok := b.(map[string]any); ok {
			band["gain"] = 0.0
		}
	}
	out["name"], out["preset_type"] = "Flat", 0
	return out
}

// headsetEQ finds a connected device's on-device EQ by name.
func (e *Engine) headsetEQ(name string) (*liveDevice, *config, map[string]any) {
	var id int
	if e.db.QueryRow(`SELECT id FROM devices WHERE name = ?`, name).Scan(&id) != nil {
		return nil, nil, nil
	}
	l := e.liveByID(id)
	if l == nil || l.primary() == nil || l.primary().Api(peqSetting) == nil {
		return nil, nil, nil
	}
	cfg, err := e.activeConfig(id)
	if err != nil {
		return nil, nil, nil
	}
	peq, _ := cfg.settingsMap()[peqSetting].(map[string]any)
	return l, cfg, peq
}

func (e *Engine) shouldFlattenEQ(name string) bool {
	e.mu.Lock()
	on := e.autoFlatten
	e.mu.Unlock()
	if on == nil || !on() {
		return false
	}
	_, _, peq := e.headsetEQ(name)
	return peq != nil && !eqIsFlat(peq)
}

func (e *Engine) flattenEQ(name string) error {
	l, cfg, peq := e.headsetEQ(name)
	if peq == nil || eqIsFlat(peq) {
		return nil
	}
	flat := flatEQ(peq)
	settings := cfg.settingsMap()
	settings[peqSetting] = flat
	b, _ := json.Marshal(settings)
	cfg.Settings = string(b)
	if err := e.saveConfig(cfg); err != nil {
		return err
	}
	_, err := e.writeSetting(l.primary(), peqSetting, flat, true)
	e.emit("reloadConfigs", map[string]any{"id": l.dbID})
	return err
}

func (e *Engine) flattenRoutes() {
	s := e.srv
	s.Handle("POST /v1/device/{name}/flattenEQ", func(w http.ResponseWriter, r *http.Request) {
		if err := e.flattenEQ(r.PathValue("name")); err != nil {
			httpx.Error(w, 500, err.Error())
			return
		}
		httpx.OK(w)
	})
	s.Handle("GET /v1/device/{name}/shouldFlattenEQ", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"shouldFlattenEQ": e.shouldFlattenEQ(r.PathValue("name"))})
	})
}
