package prism

import (
	"encoding/json"
	"io"
	"net/http"

	"ggl/internal/httpx"
)

func (s *Service) routes() {
	r := s.srv
	r.Handle("GET /sock", s.hub.ServeHTTP)

	r.Handle("GET /configs", func(w http.ResponseWriter, req *http.Request) {
		out := []map[string]any{}
		for _, c := range s.configs(`is_deleted = 0 AND is_hidden_immutable = 0`) {
			out = append(out, s.frontend(&c))
		}
		httpx.JSON(w, out)
	})
	r.Handle("GET /configs/current", func(w http.ResponseWriter, req *http.Request) {
		c := s.currentConfig()
		if c == nil {
			httpx.Error(w, 404, "no current config")
			return
		}
		httpx.JSON(w, s.frontend(c))
	})
	r.Handle("GET /configs/{id}", func(w http.ResponseWriter, req *http.Request) {
		c := s.config(req.PathValue("id"))
		if c == nil {
			httpx.Error(w, 404, "no such config")
			return
		}
		httpx.JSON(w, s.frontend(c))
	})
	r.Handle("POST /configs/create", func(w http.ResponseWriter, req *http.Request) {
		var p struct {
			Name string `json:"name"`
		}
		httpx.Decode(req, &p)
		data := "{}"
		if base := s.configs(`is_hidden_immutable = 1 LIMIT 1`); len(base) > 0 {
			b, _ := json.Marshal(base[0].Data)
			data = string(b)
		}
		id := uuid()
		s.db.Exec(`INSERT INTO configs (id, name, config_json, schema) VALUES (?, ?, ?, 1)`, id, p.Name, data)
		s.configCreated(id)
		httpx.JSON(w, map[string]any{"configId": id})
	})
	r.Handle("POST /configs/duplicate/{id}", func(w http.ResponseWriter, req *http.Request) {
		c := s.config(req.PathValue("id"))
		if c == nil {
			httpx.Error(w, 404, "no such config")
			return
		}
		var p struct {
			Name string `json:"name"`
		}
		httpx.Decode(req, &p)
		if p.Name == "" {
			p.Name = c.Name + " (copy)"
		}
		id := uuid()
		b, _ := json.Marshal(c.Data)
		s.db.Exec(`INSERT INTO configs (id, name, config_json, schema) VALUES (?, ?, ?, ?)`, id, p.Name, string(b), c.Schema)
		s.configCreated(id)
		httpx.JSON(w, map[string]any{"configId": id})
	})
	r.Handle("DELETE /configs/delete/{id}", func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("id")
		s.db.Exec(`UPDATE configs SET is_deleted = 1, is_current = 0 WHERE id = ?`, id)
		if s.currentConfig() == nil {
			s.db.Exec(`UPDATE configs SET is_current = 1 WHERE id = (SELECT id FROM configs WHERE is_deleted = 0 AND is_hidden_immutable = 0 LIMIT 1)`)
		}
		s.configChanged(id)
		s.notify("EVENT_PRISMSYNC_CONFIG_DELETED", id)
		httpx.OK(w)
	})
	// Edits from the lighting editor, to one layer of the current
	// configuration.
	r.Handle("PUT /configs/{mode}", func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(req.Body, 4<<20))
		logBody("PUT /configs/"+req.PathValue("mode"), b)
		c := s.currentConfig()
		if c == nil {
			httpx.Error(w, 404, "no current config")
			return
		}
		ups, err := parseUpdates(b)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		s.applyUpdates(c, req.PathValue("mode"), ups, parseSelection(b))
		httpx.JSON(w, s.frontend(c))
	})
	// PUT /configs/current/{id}, /configs/{id}/metadata and
	// /configs/idle/time share a shape, so they are dispatched here.
	r.Handle("PUT /configs/{a}/{b}", func(w http.ResponseWriter, req *http.Request) {
		a, b := req.PathValue("a"), req.PathValue("b")
		body, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		logBody("PUT /configs/"+a+"/"+b, body)
		switch {
		case a == "current":
			s.db.Exec(`UPDATE configs SET is_current = 0, is_last_manually_deployed = 0`)
			s.db.Exec(`UPDATE configs SET is_current = 1, is_last_manually_deployed = 1 WHERE id = ?`, b)
			s.configChanged(b)
			s.mu.Lock()
			s.current = nil
			s.mu.Unlock()
			s.notify("EVENT_PRISMSYNC_CURRENT_CONFIG_CHANGED", b)
			if c := s.config(b); c != nil {
				s.sendPreviews(c)
			}
		case a == "idle" && b == "time":
			var p struct {
				IdleTimeMs int `json:"idleTimeMs"`
			}
			json.Unmarshal(body, &p)
			if c := s.currentConfig(); c != nil {
				c.Data.IdleTimeMs = p.IdleTimeMs
				s.saveConfig(c)
			}
		case b == "metadata":
			var p struct {
				Name *string `json:"name"`
			}
			json.Unmarshal(body, &p)
			if c := s.config(a); c != nil && p.Name != nil && *p.Name != "" {
				c.Name = *p.Name
				s.saveConfig(c)
				s.notify("EVENT_PRISMSYNC_CONFIG_RENAMED", map[string]any{"id": a, "name": *p.Name})
			}
		default:
			httpx.Error(w, 404, "unknown configs route")
			return
		}
		httpx.OK(w)
	})

	r.Handle("GET /presets", func(w http.ResponseWriter, req *http.Request) {
		httpx.JSON(w, s.presets())
	})
	r.Handle("PUT /presets/{mode}/{id}", func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		logBody("PUT /presets/"+req.PathValue("mode")+"/"+req.PathValue("id"), b)
		c := s.currentConfig()
		var preset *presetRow
		for _, p := range s.presets() {
			if p.ID == req.PathValue("id") {
				preset = &p
				break
			}
		}
		if c == nil || preset == nil {
			httpx.Error(w, 404, "no such preset")
			return
		}
		sel := parseSelection(b)
		mode := req.PathValue("mode")
		if mode == "baseOrIdle" || mode == "" {
			mode = modeBase
		}
		// The client sends the dimming chosen for the preset beside it
		// (configuration_types.PresetUpdateData).
		var extra struct {
			PresetUpdateData *struct {
				DimFactorOnMonitorSleep *uint8 `json:"dimFactorOnMonitorSleep"`
			} `json:"presetUpdateData"`
			PresetUpdate *struct {
				DimFactorOnMonitorSleep *uint8 `json:"dimFactorOnMonitorSleep"`
			} `json:"presetUpdate"`
		}
		json.Unmarshal(b, &extra)
		ups := append([]update(nil), preset.Data...)
		for _, x := range []*struct {
			DimFactorOnMonitorSleep *uint8 `json:"dimFactorOnMonitorSleep"`
		}{extra.PresetUpdateData, extra.PresetUpdate} {
			if x != nil && x.DimFactorOnMonitorSleep != nil {
				for i := range ups {
					ups[i].DimFactorOnMonitorSleep = x.DimFactorOnMonitorSleep
				}
			}
		}
		s.applyUpdates(c, mode, ups, sel)
		httpx.JSON(w, s.frontend(c))
	})

	r.Handle("GET /devices", func(w http.ResponseWriter, req *http.Request) { httpx.JSON(w, s.connected()) })
	r.Handle("GET /devices/connected", func(w http.ResponseWriter, req *http.Request) { httpx.JSON(w, s.connected()) })
	r.Handle("GET /devices/count", func(w http.ResponseWriter, req *http.Request) {
		n := 0
		for _, d := range s.connected() {
			if d.Enabled {
				n++
			}
		}
		httpx.JSON(w, map[string]int{"count": n})
	})
	r.Handle("GET /devices/connected/includeDisabled/count", func(w http.ResponseWriter, req *http.Request) {
		httpx.JSON(w, map[string]int{"count": len(s.connected())})
	})
	r.Handle("GET /devices/firstTimeConnections/count", func(w http.ResponseWriter, req *http.Request) {
		n := 0
		for _, d := range s.connected() {
			if d.FirstTimeConnection {
				n++
			}
		}
		httpx.JSON(w, map[string]int{"count": n})
	})
	r.Handle("POST /devices/firstTimeConnections/clear", func(w http.ResponseWriter, req *http.Request) {
		s.db.Exec(`UPDATE device_cache SET first_connection = 0`)
		s.refreshDevices()
		httpx.OK(w)
	})
	r.Handle("GET /devices/enabled", func(w http.ResponseWriter, req *http.Request) {
		out := []map[string]any{}
		for _, d := range s.connected() {
			out = append(out, map[string]any{"id": d.ID, "fullName": d.FullName, "enabled": d.Enabled, "isFirstTimeConnection": d.FirstTimeConnection})
		}
		httpx.JSON(w, out)
	})
	r.Handle("POST /devices/enabled", func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		logBody("POST /devices/enabled", b)
		var list []struct {
			ID      int  `json:"id"`
			Enabled bool `json:"enabled"`
		}
		if json.Unmarshal(b, &list) != nil {
			var wrapped struct {
				Devices []struct {
					ID      int  `json:"id"`
					Enabled bool `json:"enabled"`
				} `json:"devices"`
			}
			json.Unmarshal(b, &wrapped)
			list = wrapped.Devices
		}
		for _, d := range list {
			s.db.Exec(`UPDATE device_cache SET enabled = ? WHERE device_id = ?`, d.Enabled, d.ID)
		}
		s.refreshDevices()
		s.notify("EVENT_PRISMSYNC_ENABLED_DEVICES_CHANGED", map[string]any{})
		httpx.OK(w)
	})
	r.Handle("POST /devices/move", func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		logBody("POST /devices/move", b)
		var p struct {
			DeviceID int `json:"deviceId"`
			ID       int `json:"id"`
			X        int `json:"canvasTranslationX"`
			Y        int `json:"canvasTranslationY"`
		}
		json.Unmarshal(b, &p)
		if p.DeviceID == 0 {
			p.DeviceID = p.ID
		}
		s.moveDevice(p.DeviceID, p.X, p.Y)
		httpx.OK(w)
	})

	r.Handle("GET /settings", func(w http.ResponseWriter, req *http.Request) {
		out := map[string]any{}
		rows, err := s.db.Query(`SELECT key, value FROM settings`)
		if err == nil {
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
		}
		httpx.JSON(w, map[string]any{"settings": out})
	})
	r.Handle("PUT /settings", func(w http.ResponseWriter, req *http.Request) {
		var p struct {
			Key   string `json:"key"`
			Value any    `json:"value"`
		}
		httpx.Decode(req, &p)
		b, _ := json.Marshal(p.Value)
		s.db.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)`, p.Key, string(b))
		if p.Key == "leftHanded" {
			s.refreshDevices()
		}
		httpx.OK(w)
	})
	r.Handle("POST /analytics/track", func(w http.ResponseWriter, req *http.Request) { httpx.OK(w) })
	r.Handle("GET /loadouts/presets", func(w http.ResponseWriter, req *http.Request) {
		httpx.JSON(w, map[string]any{"version": 1, "presets": []any{}})
	})
	r.Handle("POST /loadouts/presets/{id}/create", func(w http.ResponseWriter, req *http.Request) { httpx.JSON(w, map[string]any{"configId": ""}) })
	r.Handle("PUT /loadouts/presets/{id}/createAndDeploy", func(w http.ResponseWriter, req *http.Request) { httpx.OK(w) })
	r.Handle("GET /multimediaDevices", func(w http.ResponseWriter, req *http.Request) { httpx.JSON(w, []any{}) })
}

// notify tells the client about a change, on Prism's socket and GG's.
func (s *Service) notify(event string, data any) {
	s.hub.Broadcast(event, data)
	s.emit(event, data)
}

func (s *Service) configCreated(id string) {
	s.notify("EVENT_PRISMSYNC_CONFIG_CREATED", id)
}

// sendPreviews sends a configuration's previews, which the canvas draws.
func (s *Service) sendPreviews(c *configRow) {
	devs := s.connected()
	for _, m := range []string{modeBase, modeIdle, modeReactive} {
		s.notify("EVENT_PRISMSYNC_CONFIG_PREVIEW", s.preview(c, m, devs))
	}
}

// moveDevice stores where the user dragged a device on the canvas, as a
// plane adjustment (CanvasTranslationsToBackendAdjustmentAmount).
func (s *Service) moveDevice(id, canvasX, canvasY int) {
	s.mu.Lock()
	d := s.devices[id]
	s.mu.Unlock()
	if d == nil {
		return
	}
	dx := int32(float64(canvasX-d.CanvasTranslationX) * canvasScale)
	dy := int32(float64(canvasY-d.CanvasTranslationY) * -canvasScale)
	x, y := d.XAdjustment+dx, d.YAdjustment+dy
	s.db.Exec(`INSERT OR REPLACE INTO device_coordinate_adjustments (device_id, x_adjustment, y_adjustment) VALUES (?, ?, ?)`, id, x, y)
	s.refreshDevices()
}

// presetRow is configuration_types.Preset.
type presetRow struct {
	ID                        string   `json:"id"`
	Mode                      string   `json:"mode"`
	RequiresBitmapCoordinates bool     `json:"requiresBitmapCoordinates"`
	NameLocaleKey             string   `json:"nameLocaleKey"`
	Data                      []update `json:"data"`
}

func (s *Service) presets() []presetRow {
	rows, err := s.db.Query(`SELECT id, mode, requires_bitmap_coordinates, name_locale_key, data FROM presets`)
	out := []presetRow{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p presetRow
		var bitmap int
		var data string
		if rows.Scan(&p.ID, &p.Mode, &bitmap, &p.NameLocaleKey, &data) != nil {
			continue
		}
		p.RequiresBitmapCoordinates = bitmap != 0
		json.Unmarshal([]byte(data), &p.Data)
		if p.Data == nil {
			p.Data = []update{}
		}
		out = append(out, p)
	}
	return out
}
