package engine

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"ggl/internal/gamesense"
	"ggl/internal/httpx"
)

// Engine apps: PrismSync (Prism's lighting), ImageSync and the GameSense
// games, which GG's Apps page switches and toggle_engine_apps bindings
// turn on, off or over.

// PrismLightingEnabled reports whether Prism lights the devices (GG's
// PrismSync switch, on unless turned off).
func (e *Engine) PrismLightingEnabled() bool {
	on := true
	e.state.Get("prismLighting", &on)
	return on
}

func (e *Engine) setPrismLighting(on bool) {
	if on == e.PrismLightingEnabled() {
		return
	}
	e.state.Set("prismLighting", on)
	slog.Info("prism lighting", "on", on)
	e.emit("prismSyncEnabledChanged", map[string]any{"enabled": on})
	e.prismDevicesChanged(nil)
}

// imageSync plays a GIF on the per-key keyboards (GG's ImageSync): the
// page turns it into 22x6 frames, which run through GameSense as a
// keyboard bitmap, as the Windows Engine runs them.
type imageSync struct {
	e       *Engine
	mu      sync.Mutex
	frames  []isFrame
	preview []isFrame
	stop    chan struct{}
}

type isFrame struct {
	Image [][3]int `json:"image"`
	Delay int      `json:"delay"`
}

const (
	imageSyncGame  = "IMAGESYNC"
	imageSyncEvent = "DIRECT_KEYBOARD_IMAGESYNC"
)

func (e *Engine) imageSyncEnabled() (bool, json.RawMessage) {
	var on bool
	var data string
	if err := e.db.QueryRow(`SELECT enabled, gif_data FROM image_sync LIMIT 1`).Scan(&on, &data); err != nil {
		return false, json.RawMessage(`{"frames":[]}`)
	}
	return on, json.RawMessage(data)
}

func (e *Engine) saveImageSync(on bool, data json.RawMessage) {
	res, err := e.db.Exec(`UPDATE image_sync SET enabled = ?, gif_data = ?`, on, string(data))
	if n, _ := res.RowsAffected(); err == nil && n == 0 {
		e.db.Exec(`INSERT INTO image_sync (enabled, gif_data) VALUES (?, ?)`, on, string(data))
	}
}

// start loads the saved GIF and plays it if ImageSync is on.
func (is *imageSync) start(ctx context.Context) {
	gs := is.e.gameSense.state
	gs.Hide(imageSyncGame)
	gs.Bind(imageSyncGame, imageSyncEvent, gamesense.EventInfo{Max: 100, ValueOptional: true},
		[]json.RawMessage{json.RawMessage(`{"device-type":"rgb-per-key-zones","mode":"bitmap"}`)})
	_, data := is.e.imageSyncEnabled()
	var d struct {
		Frames []isFrame `json:"frames"`
	}
	json.Unmarshal(data, &d)
	is.mu.Lock()
	is.frames = d.Frames
	is.mu.Unlock()
	is.update()
	go func() { <-ctx.Done(); is.halt() }()
}

// update starts or stops playing for the current state.
func (is *imageSync) update() {
	on, _ := is.e.imageSyncEnabled()
	is.mu.Lock()
	frames := is.frames
	if is.preview != nil {
		frames, on = is.preview, true
	}
	is.mu.Unlock()
	is.halt()
	if !on || len(frames) == 0 {
		return
	}
	stop := make(chan struct{})
	is.mu.Lock()
	is.stop = stop
	is.mu.Unlock()
	slog.Info("imagesync: playing", "frames", len(frames))
	go is.play(frames, stop)
}

func (is *imageSync) halt() {
	is.mu.Lock()
	stop := is.stop
	is.stop = nil
	is.mu.Unlock()
	if stop != nil {
		close(stop)
		is.e.gameSense.state.Stop(imageSyncGame)
	}
}

func (is *imageSync) play(frames []isFrame, stop chan struct{}) {
	gs := is.e.gameSense.state
	for i := 0; ; i = (i + 1) % len(frames) {
		f := frames[i]
		b, _ := json.Marshal(map[string]any{"frame": map[string]any{"bitmap": bitmap22x6(f.Image)}})
		gs.Event(imageSyncGame, imageSyncEvent, b)
		delay := time.Duration(max(f.Delay, 20)) * time.Millisecond
		if len(frames) == 1 {
			delay = time.Second // a still picture: keep the game alive
		}
		select {
		case <-stop:
			return
		case <-time.After(delay):
		}
	}
}

// bitmap22x6 lays a frame out as GameSense's 22x6 keyboard picture. GG
// sends 22 colours a row (a TKL's 18 padded to 22).
func bitmap22x6(img [][3]int) [][3]int {
	out := make([][3]int, 132)
	w := 22
	if len(img) > 0 && len(img)%6 == 0 && len(img)/6 < 22 {
		w = len(img) / 6
	}
	for i, c := range img {
		x, y := i%w, i/w
		if y < 6 && x < 22 {
			out[y*22+x] = c
		}
	}
	return out
}

func (e *Engine) setImageSync(on bool) {
	_, data := e.imageSyncEnabled()
	e.saveImageSync(on, data)
	e.emit("imageSyncEnabledChanged", map[string]any{"enabled": on})
	e.imageSync.update()
}

func (e *Engine) appRoutes() {
	s := e.srv
	e.imageSync = &imageSync{e: e}
	// GG's PrismSync tile (and the old PrismSync settings it opens) reads
	// getPrismSyncInfo. Only "enabled" means anything on Linux: Prism's
	// own pages hold its lighting.
	s.Handle("GET /prismSync", func(w http.ResponseWriter, r *http.Request) {
		on := e.PrismLightingEnabled()
		effect := func() map[string]any {
			return map[string]any{"sections": []any{map[string]any{"red": 255, "green": 102, "blue": 0, "length": 255, "randomized": 0}}}
		}
		httpx.JSON(w, map[string]any{"enabled": on, "getPrismSyncInfo": map[string]any{
			"enabled":       on,
			"inactive_mode": map[string]any{"enabled": false},
			"left_handed":   false,
			"device_types": map[string]bool{"keyboard": true, "mouse": true, "headset": true, "mousepad": true,
				"motherboard": true, "monitor": true, "speakers": true, "other_rgb": true},
			"trigger_modes":    map[string]bool{"keyboard_input": true, "mouse_input": true},
			"base_effect":      effect(),
			"triggered_effect": effect(),
			"idle_info":        map[string]any{"idle_time": 0, "effect": effect()},
		}})
	})
	s.Handle("POST /prismSync/enable", func(w http.ResponseWriter, r *http.Request) { e.setPrismLighting(true); httpx.OK(w) })
	s.Handle("POST /prismSync/disable", func(w http.ResponseWriter, r *http.Request) { e.setPrismLighting(false); httpx.OK(w) })

	s.Handle("GET /imageSync", func(w http.ResponseWriter, r *http.Request) {
		on, data := e.imageSyncEnabled()
		httpx.JSON(w, map[string]any{"image_sync": map[string]any{"enabled": on, "gif_data": string(data)}, "enabled": on})
	})
	s.Handle("POST /imageSync/enable", func(w http.ResponseWriter, r *http.Request) { e.setImageSync(true); httpx.OK(w) })
	s.Handle("POST /imageSync/disable", func(w http.ResponseWriter, r *http.Request) { e.setImageSync(false); httpx.OK(w) })
	frames := func(r *http.Request) (json.RawMessage, []isFrame, bool) {
		var d struct {
			Frames []isFrame `json:"frames"`
		}
		body, err := readBody(r)
		if err != nil || json.Unmarshal(body, &d) != nil {
			return nil, nil, false
		}
		return body, d.Frames, true
	}
	s.Handle("POST /imageSync/updateGif", func(w http.ResponseWriter, r *http.Request) {
		body, fs, ok := frames(r)
		if !ok {
			httpx.Error(w, http.StatusBadRequest, "bad frames")
			return
		}
		on, _ := e.imageSyncEnabled()
		e.saveImageSync(on, body)
		e.imageSync.mu.Lock()
		e.imageSync.frames = fs
		e.imageSync.mu.Unlock()
		e.imageSync.update()
		httpx.OK(w)
	})
	s.Handle("POST /imageSync/previewGif", func(w http.ResponseWriter, r *http.Request) {
		_, fs, ok := frames(r)
		if !ok {
			httpx.Error(w, http.StatusBadRequest, "bad frames")
			return
		}
		e.imageSync.mu.Lock()
		e.imageSync.preview = fs
		e.imageSync.mu.Unlock()
		e.imageSync.update()
		httpx.OK(w)
	})
	s.Handle("POST /imageSync/stopPreviewGif", func(w http.ResponseWriter, r *http.Request) {
		e.imageSync.mu.Lock()
		e.imageSync.preview = nil
		e.imageSync.mu.Unlock()
		e.imageSync.update()
		httpx.OK(w)
	})
	// No launchable (third-party) Engine apps run on Linux.
	s.Handle("GET /engineApps/supported", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"launchableEngineAppsSupported": map[string]bool{}, "engineApps": []any{}})
	})
}

// toggleEngineApps runs a toggle_engine_apps binding: apps to turn on,
// off and over.
func (e *Engine) toggleEngineApps(params string) {
	type item struct {
		LaunchableAppID string `json:"launchable_app_id"`
		Game            string `json:"gamesense_game_name"`
		PrismSync       bool   `json:"is_prism_sync"`
		ImageSync       bool   `json:"is_image_sync"`
	}
	var p struct {
		On, Off, Toggle []item
	}
	if err := json.Unmarshal([]byte(params), &p); err != nil {
		slog.Warn("toggle engine apps", "params", params, "err", err)
		return
	}
	apply := func(it item, set func(bool) bool) {
		switch {
		case it.PrismSync:
			e.setPrismLighting(set(e.PrismLightingEnabled()))
		case it.ImageSync:
			on, _ := e.imageSyncEnabled()
			e.setImageSync(set(on))
		case it.Game != "":
			e.gameSense.setEnabled(it.Game, set(e.gameSense.state.Enabled(it.Game)))
		}
	}
	for _, it := range p.On {
		apply(it, func(bool) bool { return true })
	}
	for _, it := range p.Off {
		apply(it, func(bool) bool { return false })
	}
	for _, it := range p.Toggle {
		apply(it, func(on bool) bool { return !on })
	}
}

// gameEvent runs a game_event binding: an event for a GameSense game.
func (e *Engine) gameEvent(params string) {
	var p struct {
		Game  string `json:"game"`
		Event string `json:"event"`
		Value int    `json:"value"`
	}
	if err := json.Unmarshal([]byte(params), &p); err != nil || p.Game == "" || p.Event == "" {
		slog.Warn("game event binding", "params", params, "err", err)
		return
	}
	b, _ := json.Marshal(map[string]any{"value": p.Value})
	e.gameSense.state.Event(strings.ToUpper(p.Game), strings.ToUpper(p.Event), b)
}

func readBody(r *http.Request) ([]byte, error) { return io.ReadAll(io.LimitReader(r.Body, 16<<20)) }
