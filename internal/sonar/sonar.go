// Package sonar reimplements SteelSeries Sonar (SteelSeriesSonar.exe): the
// REST API and websocket the GG client's Sonar pages use, on Sonar's own
// database. See docs/sonar.md.
package sonar

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ggl/internal/db"
	"ggl/internal/events"
	"ggl/internal/httpx"
	"ggl/internal/paths"
)

type Service struct {
	srv   *httpx.Server
	ws    *events.Hub // ws://<sonar>/sock
	db    *sql.DB
	audio audio

	mu        sync.Mutex
	active    bool          // Sonar is enabled in GG
	stopWatch chan struct{} // stops watch
	headsets  Headsets

	fallbackMu sync.Mutex // fallback list changes
	samples    samples
	floor      *noiseFloor // the microphone's, for the automatic gate
}

func New(token string) (*Service, error) {
	d, err := db.Open(paths.SonarDB(), paths.SonarDBMig())
	if err != nil {
		return nil, err
	}
	s := &Service{srv: httpx.NewServer("sonar", token), ws: events.NewHub("sonar", ""), db: d, floor: newNoiseFloor()}
	s.audio.onRunning = s.applyChannel
	s.audio.onCopyRunning = s.applyMicCopy
	s.floor.changed = func() { s.applyVAD(vadChatCapture) }
	s.upgradeConfigs()
	s.routes()
	return s, nil
}

// Handler serves Sonar's API. Like the Windows service it is plain HTTP on
// loopback.
func (s *Service) Handler() http.Handler { return s.srv }

// Key-value settings (Sonar's key_value table).

func (s *Service) setting(key, fallback string) string {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM key_value WHERE key = ?`, key).Scan(&v); err != nil {
		return fallback
	}
	return v
}

func (s *Service) setSetting(key, value string) error {
	now := time.Now().Format(dbTime)
	_, err := s.db.Exec(`INSERT INTO key_value (key, value, created_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, now, now)
	return err
}

// reply writes a result the way Sonar's controllers do: the value as JSON,
// 400 {"error"} for argument errors and 500 for anything else.
func reply(w http.ResponseWriter, v any, err error) {
	switch {
	case err == nil:
		httpx.JSON(w, v)
	case errors.As(err, new(argError)):
		httpx.JSONStatus(w, 400, map[string]string{"error": err.Error()})
	default:
		httpx.JSONStatus(w, 500, map[string]string{"error": err.Error()})
	}
}

func decodeBody(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var ae argError
		if errors.As(err, &ae) {
			return ae
		}
		return argError{err.Error()}
	}
	return nil
}

// parseBool reads a route value the way ASP.NET binds bool.
func parseBool(s string) (bool, error) {
	b, err := strconv.ParseBool(strings.ToLower(s))
	if err != nil {
		return false, argErrorf("The value '%s' is not valid.", s)
	}
	return b, nil
}

// Modes.
const (
	modeClassic = "Classic"
	modeStream  = "Stream"
)

func (s *Service) routes() {
	sv := s.srv
	sv.Handle("GET /sock", s.ws.ServeHTTP)
	s.routingRoutes()
	s.chatMixRoutes()
	s.fallbackRoutes()
	s.sampleRoutes()
	s.streamerRoutes()
	s.deviceVolumeRoutes()

	sv.Handle("GET /configs", func(w http.ResponseWriter, r *http.Request) {
		vad := 0
		if q := r.URL.Query().Get("vad"); q != "" {
			v, ok := vadByName(q)
			if !ok {
				v, _ = strconv.Atoi(q)
			}
			if _, known := vadNames[v]; !known {
				reply(w, nil, argErrorf("The value '%s' is not valid.", q))
				return
			}
			vad = v
		}
		cfgs, err := s.configs(vad)
		reply(w, cfgs, err)
	})
	sv.Handle("POST /configs", func(w http.ResponseWriter, r *http.Request) {
		var c config
		if err := decodeBody(r, &c); err != nil {
			reply(w, nil, err)
			return
		}
		out, err := s.insertConfig(&c)
		reply(w, out, err)
	})
	sv.Handle("PUT /configs", func(w http.ResponseWriter, r *http.Request) {
		var c config
		if err := decodeBody(r, &c); err != nil {
			reply(w, nil, err)
			return
		}
		out, err := s.updateConfig(&c)
		reply(w, out, err)
	})
	sv.Handle("DELETE /configs/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.deleteConfig(r.PathValue("id"))
		reply(w, out, err)
	})
	sv.Handle("GET /configs/selected", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.selectedConfigs()
		reply(w, out, err)
	})
	sv.Handle("PUT /configs/{id}/select", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.selectConfig(r.PathValue("id"))
		reply(w, out, err)
	})
	sv.Handle("POST /configs/{id}/isFavorite/{isFavorite}", func(w http.ResponseWriter, r *http.Request) {
		fav, err := parseBool(r.PathValue("isFavorite"))
		if err != nil {
			reply(w, nil, err)
			return
		}
		out, err := s.setFavorite(r.PathValue("id"), fav)
		reply(w, out, err)
	})
	sv.Handle("POST /configs/{id}/setFavoritePosition/{position}", func(w http.ResponseWriter, r *http.Request) {
		pos, err := strconv.Atoi(r.PathValue("position"))
		if err != nil {
			reply(w, nil, argErrorf("The value '%s' is not valid.", r.PathValue("position")))
			return
		}
		out, err := s.setFavoritePosition(r.PathValue("id"), pos)
		reply(w, out, err)
	})
	sv.Handle("GET /configs/options", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, s.configOptions()) })

	sv.Handle("GET /mode", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, camel(s.setting("MODE", modeClassic))) })
	sv.Handle("PUT /mode/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		switch strings.ToLower(id) {
		case "classic", "0":
			id = modeClassic
		case "stream", "1":
			id = modeStream
		default:
			reply(w, nil, argErrorf("The value '%s' is not valid.", id))
			return
		}
		if id == s.setting("MODE", modeClassic) {
			reply(w, camel(id), nil)
			return
		}
		if err := s.setSetting("MODE", id); err != nil {
			reply(w, nil, err)
			return
		}
		if id == modeStream {
			s.enterStreamMode()
		}
		s.stopSamples()
		s.syncAudio()
		// The client fetches the stream redirections again on this event;
		// entering streamer mode may have set its devices.
		s.ws.Broadcast("SONAR_EVENT_REDIRECTION_STATUS_UPDATE", nil)
		reply(w, camel(id), nil)
	})

	// Features follow the connected SteelSeries devices (hardware ChatMix,
	// GameDAC outputs...); none are enabled yet.
	sv.Handle("GET /features", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, []any{}) })
	sv.Handle("GET /features/development", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, []any{}) })
	sv.Handle("GET /features/{feature}", func(w http.ResponseWriter, r *http.Request) {
		// BuiltInEq's data lists the devices with their own EQ that Sonar
		// plays to (the "double EQ" warning).
		var data any
		if strings.EqualFold(r.PathValue("feature"), "BuiltInEq") {
			data = []any{}
		}
		httpx.JSON(w, map[string]any{"id": r.PathValue("feature"), "isEnabled": false, "data": data})
	})
	s.statusRoutes()
	s.redirectionRoutes()
}
