// Package prism reimplements the PrismSync V2 service (SteelSeriesPrism.exe),
// GG's "Lighting / Bloom" sub-app: lighting configurations shared across
// every RGB device, their presets, and which devices take part.
package prism

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"

	"ggl/internal/db"
	"ggl/internal/events"
	"ggl/internal/httpx"
)

// Devices supplies the RGB devices the Engine knows about.
type Devices interface {
	PrismDevices() []map[string]any
}

type Service struct {
	srv     *httpx.Server
	hub     *events.Hub
	db      *sql.DB
	devices Devices
	emit    func(event string, data any)
	mu      sync.Mutex
}

func New(dbPath, migrations string, devices Devices, emit func(string, any)) (*Service, error) {
	d, err := db.Open(dbPath, migrations)
	if err != nil {
		return nil, err
	}
	s := &Service{srv: httpx.NewServer("prism", ""), hub: events.NewHub("prism-sock", ""), db: d, devices: devices, emit: emit}
	s.routes()
	return s, nil
}

func (s *Service) Handler() http.Handler { return s.srv }

type configRow struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	IsCurrent    bool            `json:"is_current"`
	IsDeleted    bool            `json:"isDeleted"`
	IsHidden     bool            `json:"isHiddenImmutable"`
	IsLastDeploy bool            `json:"isLastManuallyDeployed"`
	IsPerDevice  bool            `json:"isPerDevice"`
	Schema       int             `json:"schema"`
	Data         json.RawMessage `json:"configurationData"`
	IdleTimeMs   int             `json:"idleTimeMs"`
}

func (s *Service) configs(where string, args ...any) []configRow {
	rows, err := s.db.Query(`SELECT id, name, is_current, is_deleted, is_hidden_immutable, is_last_manually_deployed,
		is_per_device, schema, config_json FROM configs WHERE `+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []configRow{}
	for rows.Next() {
		var c configRow
		var cur, del, hid, last, per int
		var data string
		if rows.Scan(&c.ID, &c.Name, &cur, &del, &hid, &last, &per, &c.Schema, &data) != nil {
			continue
		}
		c.IsCurrent, c.IsDeleted, c.IsHidden, c.IsLastDeploy, c.IsPerDevice = cur != 0, del != 0, hid != 0, last != 0, per != 0
		if !json.Valid([]byte(data)) {
			data = "{}"
		}
		c.Data = json.RawMessage(data)
		var d struct {
			IdleTimeMs int `json:"idleTimeMs"`
		}
		json.Unmarshal(c.Data, &d)
		c.IdleTimeMs = d.IdleTimeMs
		out = append(out, c)
	}
	return out
}

// frontend renders configuration_types.ConfigurationDataForFrontend.
func frontend(c configRow) map[string]any {
	return map[string]any{
		"id": c.ID, "name": c.Name, "is_current": c.IsCurrent, "idleTimeMs": c.IdleTimeMs,
		"zones": map[string]any{}, "graphics": map[string]any{},
		"basePreview": map[string]any{}, "idlePreview": map[string]any{}, "reactivePreview": map[string]any{},
		"configurationData": c.Data,
	}
}

func (s *Service) setting(key string) string {
	var v string
	s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v
}

func (s *Service) routes() {
	r := s.srv
	r.Handle("GET /sock", s.hub.ServeHTTP)
	r.Handle("GET /configs", func(w http.ResponseWriter, req *http.Request) {
		out := []map[string]any{}
		for _, c := range s.configs(`is_deleted = 0 AND is_hidden_immutable = 0`) {
			out = append(out, frontend(c))
		}
		httpx.JSON(w, out)
	})
	r.Handle("GET /configs/current", func(w http.ResponseWriter, req *http.Request) {
		cs := s.configs(`is_current = 1 AND is_deleted = 0`)
		if len(cs) == 0 {
			httpx.Error(w, 404, "no current config")
			return
		}
		httpx.JSON(w, frontend(cs[0]))
	})
	r.Handle("GET /configs/{id}", func(w http.ResponseWriter, req *http.Request) {
		cs := s.configs(`id = ?`, req.PathValue("id"))
		if len(cs) == 0 {
			httpx.Error(w, 404, "no such config")
			return
		}
		httpx.JSON(w, frontend(cs[0]))
	})
	r.Handle("POST /configs/create", func(w http.ResponseWriter, req *http.Request) {
		var p struct {
			Name string `json:"name"`
		}
		httpx.Decode(req, &p)
		base := s.configs(`is_hidden_immutable = 1 LIMIT 1`)
		data := "{}"
		if len(base) > 0 {
			data = string(base[0].Data)
		}
		id := uuid()
		s.db.Exec(`INSERT INTO configs (id, name, config_json, schema) VALUES (?, ?, ?, 1)`, id, p.Name, data)
		s.emit("EVENT_PRISMSYNC_CONFIG_CREATED", map[string]any{"configId": id})
		httpx.JSON(w, map[string]any{"configId": id})
	})
	r.Handle("POST /configs/duplicate/{id}", func(w http.ResponseWriter, req *http.Request) {
		cs := s.configs(`id = ?`, req.PathValue("id"))
		if len(cs) == 0 {
			httpx.Error(w, 404, "no such config")
			return
		}
		id := uuid()
		s.db.Exec(`INSERT INTO configs (id, name, config_json, schema) VALUES (?, ?, ?, ?)`, id, cs[0].Name+" (copy)", string(cs[0].Data), cs[0].Schema)
		s.emit("EVENT_PRISMSYNC_CONFIG_CREATED", map[string]any{"configId": id})
		httpx.JSON(w, map[string]any{"configId": id})
	})
	r.Handle("DELETE /configs/delete/{id}", func(w http.ResponseWriter, req *http.Request) {
		s.db.Exec(`UPDATE configs SET is_deleted = 1, is_current = 0 WHERE id = ?`, req.PathValue("id"))
		if len(s.configs(`is_current = 1 AND is_deleted = 0`)) == 0 {
			s.db.Exec(`UPDATE configs SET is_current = 1 WHERE id = (SELECT id FROM configs WHERE is_deleted = 0 AND is_hidden_immutable = 0 LIMIT 1)`)
		}
		s.emit("EVENT_PRISMSYNC_CONFIG_DELETED", map[string]any{"configId": req.PathValue("id")})
		httpx.OK(w)
	})
	// Edits from the lighting editor arrive per layer ("base", "idle",
	// "reactive"). They are stored on the current config.
	r.Handle("PUT /configs/{mode}", func(w http.ResponseWriter, req *http.Request) {
		var p map[string]any
		httpx.Decode(req, &p)
		cs := s.configs(`is_current = 1 AND is_deleted = 0`)
		if len(cs) > 0 {
			var data map[string]any
			json.Unmarshal(cs[0].Data, &data)
			if data == nil {
				data = map[string]any{}
			}
			if upd, ok := p["configurationUpdateData"]; ok {
				data[req.PathValue("mode")+"Update"] = upd
			}
			b, _ := json.Marshal(data)
			s.db.Exec(`UPDATE configs SET config_json = ? WHERE id = ?`, string(b), cs[0].ID)
			s.emit("EVENT_PRISMSYNC_CONFIG_PREVIEW", map[string]any{"configId": cs[0].ID})
		}
		httpx.OK(w)
	})
	// PUT /configs/current/{id}, /configs/{id}/metadata and
	// /configs/idle/time share a shape, so they are dispatched here.
	r.Handle("PUT /configs/{a}/{b}", func(w http.ResponseWriter, req *http.Request) {
		a, b := req.PathValue("a"), req.PathValue("b")
		switch {
		case a == "current":
			s.db.Exec(`UPDATE configs SET is_current = 0, is_last_manually_deployed = 0`)
			s.db.Exec(`UPDATE configs SET is_current = 1, is_last_manually_deployed = 1 WHERE id = ?`, b)
			s.emit("EVENT_PRISMSYNC_CURRENT_CONFIG_CHANGED", map[string]any{"configId": b})
		case a == "idle" && b == "time":
			var p struct {
				IdleTimeMs int `json:"idleTimeMs"`
			}
			httpx.Decode(req, &p)
			s.db.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('idleTimeMs', ?)`, strconv.Itoa(p.IdleTimeMs))
		case b == "metadata":
			var p struct {
				Name string `json:"name"`
			}
			httpx.Decode(req, &p)
			if p.Name != "" {
				s.db.Exec(`UPDATE configs SET name = ? WHERE id = ?`, p.Name, a)
				s.emit("EVENT_PRISMSYNC_CONFIG_RENAMED", map[string]any{"configId": a, "name": p.Name})
			}
		default:
			httpx.Error(w, 404, "unknown configs route")
			return
		}
		httpx.OK(w)
	})
	r.Handle("GET /presets", func(w http.ResponseWriter, req *http.Request) {
		rows, err := s.db.Query(`SELECT id, mode, requires_bitmap_coordinates, name_locale_key, data FROM presets`)
		out := []map[string]any{}
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id, mode, key, data string
				var bitmap int
				if rows.Scan(&id, &mode, &bitmap, &key, &data) == nil {
					out = append(out, map[string]any{"id": id, "mode": mode, "requiresBitmapCoordinates": bitmap != 0,
						"nameLocaleKey": key, "data": json.RawMessage(data)})
				}
			}
		}
		httpx.JSON(w, out)
	})
	r.Handle("PUT /presets/{mode}/{id}", func(w http.ResponseWriter, req *http.Request) { httpx.OK(w) })

	devices := func() []map[string]any {
		if s.devices == nil {
			return []map[string]any{}
		}
		return s.devices.PrismDevices()
	}
	r.Handle("GET /devices", func(w http.ResponseWriter, req *http.Request) { httpx.JSON(w, devices()) })
	r.Handle("GET /devices/connected", func(w http.ResponseWriter, req *http.Request) { httpx.JSON(w, devices()) })
	r.Handle("GET /devices/count", func(w http.ResponseWriter, req *http.Request) { httpx.JSON(w, map[string]int{"count": len(devices())}) })
	r.Handle("GET /devices/connected/includeDisabled/count", func(w http.ResponseWriter, req *http.Request) {
		httpx.JSON(w, map[string]int{"count": len(devices())})
	})
	r.Handle("GET /devices/firstTimeConnections/count", func(w http.ResponseWriter, req *http.Request) { httpx.JSON(w, map[string]int{"count": 0}) })
	r.Handle("POST /devices/firstTimeConnections/clear", func(w http.ResponseWriter, req *http.Request) { httpx.OK(w) })
	r.Handle("POST /devices/move", func(w http.ResponseWriter, req *http.Request) { httpx.OK(w) })
	r.Handle("GET /devices/enabled", func(w http.ResponseWriter, req *http.Request) {
		out := []map[string]any{}
		for _, d := range devices() {
			out = append(out, map[string]any{"id": d["id"], "isEnabled": true, "isFirstTimeConnection": false})
		}
		httpx.JSON(w, out)
	})
	r.Handle("POST /devices/enabled", func(w http.ResponseWriter, req *http.Request) {
		s.emit("EVENT_PRISMSYNC_ENABLED_DEVICES_CHANGED", map[string]any{})
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

func uuid() string {
	return httpx.RandomToken(4) + "-" + httpx.RandomToken(2) + "-" + httpx.RandomToken(2) + "-" + httpx.RandomToken(2) + "-" + httpx.RandomToken(6)
}
