package engine

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"ggl/internal/devicert"
	"ggl/internal/httpx"
)

// Bindings: a configuration's keys and buttons bound to something other
// than their mapping (domains.Binding): a recorded macro, a transparent
// macro (another key or button), an OS shortcut (copy, paste...) or an
// Engine command. GG sends them with the configuration and keeps them in
// the bindings table.
//
// Keyboards with onboard macros (the Apex 7) play macro, transparent
// macro and OS shortcut bindings themselves: their specs ask for them
// when the key mappings are written (devicert's MacroSource).

// binding is a row of the bindings table, as GG reads it.
type binding struct {
	ID                   int    `json:"id"`
	Trigger              string `json:"trigger"`
	Type                 string `json:"type"`
	MacroID              string `json:"macro_id"`
	SseCommandID         string `json:"sse_command_id"`
	TransparentMacroData string `json:"transparent_macro_data"`
	PlaybackOptionsData  string `json:"playback_options_data"`
	OSMacroType          string `json:"os_macro_type"`
	IsPassthrough        bool   `json:"is_passthrough"`
}

const defaultPlayback = `{ "type": 0, "num_repeat": 1, "repeat_delay": 15}`

// bindingsOf reads a configuration's bindings.
func (e *Engine) bindingsOf(configID string) []binding {
	rows, err := e.db.Query(`SELECT id, type, CAST(coalesce(macro_id, '') AS TEXT), trigger_data, CAST(coalesce(sse_command_id, '') AS TEXT),
		transparent_macro_data, playback_options_data, os_macro_type, is_passthrough FROM bindings WHERE config_id = ? ORDER BY id`, configID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []binding
	for rows.Next() {
		var b binding
		if rows.Scan(&b.ID, &b.Type, &b.MacroID, &b.Trigger, &b.SseCommandID, &b.TransparentMacroData, &b.PlaybackOptionsData, &b.OSMacroType, &b.IsPassthrough) != nil {
			continue
		}
		if b.MacroID == "0" {
			b.MacroID = ""
		}
		if b.SseCommandID == "0" {
			b.SseCommandID = ""
		}
		out = append(out, b)
	}
	return out
}

// withBindings fills in a configuration's bindings for GG.
func (e *Engine) withBindings(c *config) *config {
	c.Bindings = []any{}
	for _, b := range e.bindingsOf(c.ID) {
		c.Bindings = append(c.Bindings, b)
	}
	return c
}

// setBindings replaces a configuration's bindings with the ones GG sent
// (each binding's toJSON: trigger, playback options and transparent macro
// as JSON text).
func (e *Engine) setBindings(configID string, raw json.RawMessage) error {
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM bindings WHERE config_id = ?`, configID); err != nil {
		return err
	}
	for _, m := range list {
		macroID, sseID := nullable(text(m["macro_id"])), nullable(text(m["sse_command_id"]))
		typ := text(m["type"])
		if typ == "" && macroID != nil {
			typ = "macro"
		}
		playback := text(m["playback_options_data"])
		if playback == "" {
			playback = defaultPlayback
		}
		var passthrough bool
		json.Unmarshal(m["is_passthrough"], &passthrough)
		if _, err := tx.Exec(`INSERT INTO bindings (type, config_id, macro_id, trigger_data, sse_command_id, transparent_macro_data, playback_options_data, is_passthrough, os_macro_type)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, typ, configID, macroID, orEmpty(text(m["trigger"]), "{}"), sseID,
			text(m["transparent_macro_data"]), playback, passthrough, text(m["os_macro_type"])); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// text reads a binding field GG sends as a string, a number or an object
// (an object is kept as its JSON text).
func text(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

func nullable(s string) any {
	if s == "" || s == "0" {
		return nil
	}
	return s
}

func orEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// MacroBindings gives the onboard-playable bindings of a configuration
// (devicert.MacroSource): configID, or the device's current one.
func (e *Engine) MacroBindings(devID uint32, configID string) []devicert.MacroBinding {
	if configID == "" {
		l := e.liveByProduct(devID)
		if l == nil {
			return nil
		}
		cfg, err := e.activeConfig(l.dbID)
		if err != nil {
			return nil
		}
		configID = cfg.ID
	}
	var out []devicert.MacroBinding
	for _, b := range e.bindingsOf(configID) {
		mb, ok := e.macroBinding(b)
		if ok {
			out = append(out, mb)
		}
	}
	return out
}

// macroBinding turns a binding into the macro a device would play for it.
func (e *Engine) macroBinding(b binding) (devicert.MacroBinding, bool) {
	var mb devicert.MacroBinding
	if json.Unmarshal([]byte(b.Trigger), &mb.Trigger) != nil {
		return mb, false
	}
	mb.Playback = devicert.Playback{NumRepeats: 1, RepeatDelay: 15}
	json.Unmarshal([]byte(orEmpty(b.PlaybackOptionsData, defaultPlayback)), &playbackJSON{&mb.Playback})
	switch {
	case b.MacroID != "":
		var events string
		if e.db.QueryRow(`SELECT coalesce(events, '[]') FROM macros WHERE id = ? AND is_deleted = 0`, b.MacroID).Scan(&events) != nil {
			return mb, false
		}
		if err := json.Unmarshal([]byte(events), &mb.Events); err != nil {
			slog.Warn("macro events", "macro", b.MacroID, "err", err)
			return mb, false
		}
	case b.Type == "transparent_macro":
		var t transparentMacro
		if json.Unmarshal([]byte(b.TransparentMacroData), &t) != nil {
			return mb, false
		}
		events, ok := transparentEvents(mb.Trigger, t, mb.Playback)
		if !ok {
			return mb, false
		}
		mb.Events = events
	case b.Type == "os_macro":
		events := osMacroEvents(b.OSMacroType)
		if events == nil {
			slog.Warn("unknown OS macro", "type", b.OSMacroType)
			return mb, false
		}
		mb.Events = events
	default:
		return mb, false // Engine commands run in software
	}
	return mb, true
}

// playbackJSON reads playback options, whose numbers GG sometimes sends
// as strings.
type playbackJSON struct{ p *devicert.Playback }

func (p *playbackJSON) UnmarshalJSON(b []byte) error {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	get := func(k string) (int, bool) {
		switch v := m[k].(type) {
		case float64:
			return int(v), true
		case string:
			n, err := strconv.Atoi(v)
			return n, err == nil
		}
		return 0, false
	}
	if v, ok := get("type"); ok {
		p.p.Type = uint8(v)
	}
	if v, ok := get("num_repeat"); ok {
		p.p.NumRepeats = uint8(min(max(v, 0), 255))
	}
	if v, ok := get("repeat_delay"); ok {
		p.p.RepeatDelay = uint16(min(max(v, 0), 65535))
	}
	return nil
}

// transparentMacro is domains.TransparentMacroData: the key or button a
// binding stands in for.
type transparentMacro struct {
	HIDCode uint16 `json:"hidCode"`
	Page    uint8  `json:"page"`
	Type    uint8  `json:"type"`
}

// transparentEvents is the Windows Engine's
// transformTransparentMacroBindingIntoEvents: the events of a binding to
// another key or button; ok false for a key bound to itself.
func transparentEvents(trig devicert.Trigger, t transparentMacro, p devicert.Playback) ([]devicert.MacroEvent, bool) {
	if trig.HIDCode == t.HIDCode && trig.Page == t.Page &&
		(trig.Type == t.Type || trig.Type == 0 && t.Type == devicert.EventMouseHold ||
			trig.Type == 2 && t.Type == devicert.EventKeyHold && p.Type == 0 && trig.Direction == 1) {
		return nil, false
	}
	ev := func(typ uint8, code uint16, data int64) devicert.MacroEvent {
		return devicert.MacroEvent{Type: typ, Page: t.Page, Code: code, ExtraData: data}
	}
	switch {
	case t.Type == 0 && t.HIDCode == 0 && t.Page == 0:
		return []devicert.MacroEvent{}, true // disabled
	case t.Type == devicert.EventMouseWheel:
		first := ev(devicert.EventMouseWheel, t.HIDCode, 0)
		switch t.HIDCode {
		case 0x31:
			first.Code, first.ExtraData = 0, 1
		case 0x32:
			first.Code, first.ExtraData = 0, -1
		}
		return []devicert.MacroEvent{first, ev(devicert.EventMouseWheel, 0, 0)}, true
	case p.Type >= 1 && p.Type <= 3:
		typ := t.Type
		switch typ {
		case devicert.EventMouseHold:
			typ = devicert.EventMouseButton
		case devicert.EventKeyHold:
			typ = devicert.EventKey
		}
		return []devicert.MacroEvent{ev(typ, t.HIDCode, 1), {Type: devicert.EventDelay, ExtraData: 1}, ev(typ, t.HIDCode, 0)}, true
	}
	return []devicert.MacroEvent{ev(t.Type, t.HIDCode, 0)}, true
}

// HID keyboard usages the OS shortcuts use.
const (
	hidLeftCtrl  = 0xe0
	hidLeftShift = 0xe1
	hidLeftAlt   = 0xe2
	hidLeftGUI   = 0xe3
)

// osMacros are GG's OS shortcuts, as the Windows Engine types them: the
// modifiers held around a key.
var osMacros = map[string][]uint16{
	"copy":              {hidLeftCtrl, 0x06},
	"cut":               {hidLeftCtrl, 0x1b},
	"paste":             {hidLeftCtrl, 0x19},
	"undo":              {hidLeftCtrl, 0x1d},
	"redo":              {hidLeftCtrl, hidLeftShift, 0x1d},
	"close_window":      {hidLeftCtrl, 0x1a},
	"close_application": {hidLeftAlt, 0x3d},
	"lock":              {hidLeftGUI, 0x0f},
	"show_desktop":      {hidLeftGUI, 0x07},
	"task_manager":      {hidLeftCtrl, hidLeftShift, 0x29},
	"capture_window":    {hidLeftAlt, 0x46},
	"run":               {hidLeftGUI, 0x15},
	"explorer":          {hidLeftGUI, 0x08},
	"minimize":          {hidLeftGUI, 0x51},
	"maximize":          {hidLeftGUI, 0x52},
	"taskbar":           {hidLeftGUI, 0x17},
	"system_info":       {hidLeftGUI, 0x48},
	"language":          {hidLeftGUI, 0x2c},
	"align_left":        {hidLeftGUI, 0x50},
	"align_right":       {hidLeftGUI, 0x4f},
	"monitor_left":      {hidLeftGUI, hidLeftShift, 0x50},
	"monitor_right":     {hidLeftGUI, hidLeftShift, 0x4f},
	"switch_apps":       {hidLeftAlt, 0x2b},
}

// osMacroEvents types an OS shortcut: the keys down in order, 20 ms, then
// up in reverse.
func osMacroEvents(name string) []devicert.MacroEvent {
	keys, ok := osMacros[name]
	if !ok {
		return nil
	}
	key := func(code uint16, down int64) devicert.MacroEvent {
		return devicert.MacroEvent{Type: devicert.EventKey, Page: 1, Code: code, ExtraData: down}
	}
	var out []devicert.MacroEvent
	for _, k := range keys {
		out = append(out, key(k, 1))
	}
	out = append(out, devicert.MacroEvent{Type: devicert.EventDelay, ExtraData: 20})
	for i := len(keys) - 1; i >= 0; i-- {
		out = append(out, key(keys[i], 0))
	}
	return out
}

// configsUsingMacro lists the configurations with a key bound to a macro,
// as GG's macro editor asks before deleting or after changing it (only
// those of devices with onboard macros when onboard is set).
func (e *Engine) configsUsingMacro(macroID string, onboard bool) []map[string]any {
	rows, err := e.db.Query(`SELECT DISTINCT config_id FROM bindings WHERE macro_id = ?`, macroID)
	if err != nil {
		return []map[string]any{}
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	out := []map[string]any{}
	for _, id := range ids {
		c, err := e.configByID(id)
		if err != nil || c.IsDeleted != 0 {
			continue
		}
		cat, err := e.catalogByID(c.DeviceID)
		if err != nil || onboard && !cat.HasOnboardMacros {
			continue
		}
		out = append(out, map[string]any{"config": c, "device_name": cat.Name})
	}
	return out
}

func (e *Engine) macroConfigRoutes() {
	e.srv.Handle("GET /macro/{mid}/configs", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"configs": e.configsUsingMacro(r.PathValue("mid"), false)})
	})
	e.srv.Handle("GET /macro/{mid}/onBoardConfigs", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"configs": e.configsUsingMacro(r.PathValue("mid"), true)})
	})
}
