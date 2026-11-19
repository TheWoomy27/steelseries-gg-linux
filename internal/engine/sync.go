package engine

import (
	"encoding/json"
	"log/slog"
	"strings"

	"ggl/internal/devicert"
)

// listenSync forwards reports from a device's sync interface to the spec.
// Devices push battery, connection, chat mix and on-device setting changes
// there; the spec turns them into device events and settings updates.
func (e *Engine) listenSync(l *liveDevice, d *devicert.Decl) {
	node := e.hid.Find(d.ID, d.SyncUsagePage, d.SyncUsage, d.SyncNum)
	if node == nil {
		node = e.hid.Find(d.ID, d.SyncUsagePage, d.SyncUsage, -1)
	}
	if node == nil {
		slog.Debug("sync interface not found", "spec", d.Name)
		return
	}
	reports := make(chan []byte, 64)
	node.OnEvent(func(r []byte) {
		select {
		case reports <- r:
		default:
		}
	})
	go func() {
		for r := range reports {
			if !d.Connected() {
				return
			}
			settings, err := d.TranslateSync(r)
			if err != nil {
				slog.Debug("translate_sync_data", "spec", d.Name, "err", err)
				continue
			}
			e.applyDeviceSettings(l, d, settings)
		}
	}()
}

// applyDeviceSettings merges settings read from the device into the active
// configuration without writing them back, then tells the client.
func (e *Engine) applyDeviceSettings(l *liveDevice, d *devicert.Decl, raw string) {
	changed := parseSettings(raw)
	if len(changed) == 0 {
		return
	}
	cfg, err := e.activeConfig(l.dbID)
	if err != nil {
		return
	}
	e.mergeIntoConfig(cfg, d, changed)
}

// syncToConfig keeps a setting changed on the device itself (an Apex
// keyboard's actuation or rapid trigger set from its own menu) in a
// configuration: the spec's update_config_settings names it, or the
// active one when it names none. GG writes the configuration again when
// the device reconnects, so a change it didn't keep would be undone.
func (e *Engine) syncToConfig(l *liveDevice, configID string, settings map[string]any) {
	if len(settings) == 0 {
		return
	}
	active, err := e.activeConfig(l.dbID)
	if err != nil {
		return
	}
	cfg := active
	if configID != "" && configID != active.ID {
		if cfg, err = e.configByID(configID); err != nil || cfg.DeviceID != l.dbID {
			return
		}
	}
	var d *devicert.Decl
	if cfg.ID == active.ID {
		d = l.primary() // it is on the device now
	}
	e.mergeIntoConfig(cfg, d, settings)
}

// mergeIntoConfig merges settings into a configuration as the Engine
// merges what devices report (MergeJSONMap): objects field by field,
// lists entry by entry, so a report about some keys leaves the others
// as they were. d, when given, has the result as its values on the
// device.
func (e *Engine) mergeIntoConfig(cfg *config, d *devicert.Decl, changed map[string]any) {
	settings := cfg.settingsMap()
	sent := map[string]any{}
	for api, v := range changed {
		merged := mergeSetting(settings[api], v)
		settings[api], sent[api] = merged, merged
		if m, ok := merged.(map[string]any); ok && d != nil {
			d.SetCachedValues(api, m)
		}
	}
	b, _ := json.Marshal(settings)
	cfg.Settings = string(b)
	if err := e.saveConfig(cfg); err != nil {
		return
	}
	// The client merges these into the configuration it shows; whole
	// values, as a list of keys only names the ones that changed.
	out, _ := json.Marshal(sent)
	e.emit("configSettingsChanged", map[string]any{"id": cfg.ID, "settings": string(out)})
}

func parseSettings(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	return m
}

// mergeSetting merges a reported value into a configuration's: objects
// field by field, lists entry by entry (entries for a key, which carry
// its hid, by that hid, others by position), anything else replaced.
func mergeSetting(cur, v any) any {
	switch x := v.(type) {
	case map[string]any:
		c, _ := cur.(map[string]any)
		out := make(map[string]any, len(c)+len(x))
		for k, cv := range c {
			out[k] = cv
		}
		for k, xv := range x {
			out[k] = mergeSetting(c[k], xv)
		}
		return out
	case []any:
		c, ok := cur.([]any)
		if !ok {
			return x
		}
		out := append([]any{}, c...)
		for i, xv := range x {
			j := entryFor(out, xv, i)
			if j < 0 {
				out = append(out, xv)
				continue
			}
			out[j] = mergeSetting(out[j], xv)
		}
		return out
	}
	return v
}

// entryFor finds the entry of list that v, the i-th reported entry, is
// about.
func entryFor(list []any, v any, i int) int {
	if m, ok := v.(map[string]any); ok {
		if hid, ok := number(m["hid"]); ok {
			for j, e := range list {
				if em, ok := e.(map[string]any); ok {
					if h, ok := number(em["hid"]); ok && h == hid {
						return j
					}
				}
			}
			return -1
		}
	}
	if i < len(list) {
		return i
	}
	return -1
}
