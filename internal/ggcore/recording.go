package ggcore

import (
	"log/slog"
	"net/http"
	"time"

	"ggl/internal/httpx"
	"ggl/internal/inputrec"
)

// Input recording: GG's macro editor, its key binding fields and its
// shortcut settings record keys through GG core, which sends each key or
// button event as recordedDeviceEvent, and stoppedDeviceEventRecording
// when the recording runs out of time (the client stops it otherwise).

// maxRecording bounds a recording the client never stops.
const maxRecording = 10 * time.Minute

func (c *Core) recordingRoutes() {
	s := c.srv
	s.Handle("POST /startMacroRecording", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Timeout float64 `json:"timeout"` // seconds
		}
		httpx.Decode(r, &p)
		timeout := maxRecording
		if p.Timeout > 0 {
			timeout = min(time.Duration(p.Timeout*float64(time.Second)), maxRecording)
		}
		err := c.recorder.Start(timeout,
			func(ev inputrec.Event) { c.Broadcast("recordedDeviceEvent", ev) },
			func() { c.Broadcast("stoppedDeviceEventRecording", map[string]any{}) })
		if err != nil {
			slog.Warn("input recording", "err", err)
			httpx.Error(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		httpx.OK(w)
	})
	s.Handle("POST /stopMacroRecording", func(w http.ResponseWriter, r *http.Request) {
		c.recorder.Stop()
		httpx.OK(w)
	})
}
