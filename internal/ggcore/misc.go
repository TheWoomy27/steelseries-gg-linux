package ggcore

import (
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"syscall"

	"ggl/internal/httpx"
)

func (c *Core) miscRoutes() {
	s := c.srv
	s.Handle("GET /startup/allowed", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"allowed": true})
	})
	s.Handle("GET /testFlags", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"flags": map[string]bool{
			"useHomepageTestData": false, "useGiveawaysTestData": false, "useEngineTestData": false,
			"useAudioXpFutureDevices": false, "useSingleEndpointXp": false, "useAudioLiteNova7": false,
			"useEnableAppsTray": true,
		}})
	})
	s.Handle("GET /abTests", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"tests": []any{}})
	})
	// Telemetry is accepted and discarded.
	for _, p := range []string{"/analytics/track", "/analytics/trackWithoutGG"} {
		s.Handle("POST "+p, func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	}
	s.Handle("GET /applicationMetadata", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"applicationMetadata": map[string]any{}})
	})

	s.Handle("GET /keyboardShortcuts", func(w http.ResponseWriter, r *http.Request) {
		ks := map[string]any{}
		c.db.Get("keyboardShortcuts", &ks)
		httpx.JSON(w, map[string]any{"keyboardShortcuts": ks})
	})
	s.Handle("POST /keyboardShortcuts", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[map[string]any](r)
		ks := map[string]any{}
		c.db.Get("keyboardShortcuts", &ks)
		for k, v := range p {
			ks[k] = v
		}
		c.db.Set("keyboardShortcuts", ks)
		httpx.JSON(w, map[string]any{"keyboardShortcuts": ks})
	})

	// Updates come from the distribution / gg-setup, not an in-app updater.
	s.Handle("POST /update/check", func(w http.ResponseWriter, r *http.Request) {
		c.Broadcast("EVENT_APP_UPDATE_VERSION_IS_CURRENT", map[string]any{})
		httpx.JSON(w, map[string]any{"updateAvailable": false})
	})
	s.Handle("POST /update/install", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusNotImplemented, "update GG by re-running gg-setup with a newer installer")
	})

	s.Handle("POST /startClient", func(w http.ResponseWriter, r *http.Request) {
		c.StartClient()
		httpx.OK(w)
	})
	s.Handle("POST /shutdownClient", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("GET /clientThreadId", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"clientThreadId": 0})
	})
	s.Handle("POST /shutdown", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w)
		go syscall.Kill(os.Getpid(), syscall.SIGTERM)
	})
	s.Handle("POST /deepLink/generate", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			Path string `json:"path"`
		}](r)
		httpx.JSON(w, map[string]any{"deepLink": "steelseriesgg://" + p.Path})
	})
	s.Handle("POST /cvGamesense/start", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusNotImplemented, "CV GameSense is not available on Linux")
	})
	s.Handle("POST /cvGamesense/stop", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })

	// GameSense and overlay registries (raw JSON arrays, as on Windows).
	for _, p := range []string{"/overlays", "/game_integration_games", "/game_integration_devices", "/known_games"} {
		s.Handle("GET "+p, func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, []any{}) })
	}

	// Engine -> core notifications.
	for _, p := range []string{"/engine/device/connected", "/engine/device/disconnected", "/engine/device/preConnect", "/engine/device/preConnectionsComplete", "/cacheDevices"} {
		s.Handle("POST "+p, func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	}
}

// StartClient launches the Electron client (tray "Open" and /startClient).
func (c *Core) StartClient() {
	bin := os.Getenv("GG_LINUX_CLIENT")
	if bin == "" {
		bin = "steelseries-gg"
	}
	cmd := exec.Command(bin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		slog.Error("start client", "bin", bin, "err", err)
		return
	}
	go cmd.Wait()
}
