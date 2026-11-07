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
	node := e.hid.Find(uint16(d.ID&0xffff), d.SyncUsagePage, d.SyncUsage, d.SyncNum)
	if node == nil {
		node = e.hid.Find(uint16(d.ID&0xffff), d.SyncUsagePage, d.SyncUsage, -1)
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
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return
	}
	var changed map[string]any
	if err := json.Unmarshal([]byte(raw), &changed); err != nil || len(changed) == 0 {
		return
	}
	cfg, err := e.activeConfig(l.dbID)
	if err != nil {
		return
	}
	settings := cfg.settingsMap()
	for api, v := range changed {
		vals, ok := v.(map[string]any)
		if !ok {
			settings[api] = v
			continue
		}
		cur, _ := settings[api].(map[string]any)
		if cur == nil {
			cur = map[string]any{}
		}
		for k, x := range vals {
			cur[k] = x
		}
		settings[api] = cur
		d.SetCachedValues(api, cur)
	}
	b, _ := json.Marshal(settings)
	cfg.Settings = string(b)
	if err := e.saveConfig(cfg); err != nil {
		return
	}
	e.emit("configSettingsChanged", map[string]any{"id": cfg.ID, "settings": raw})
}
