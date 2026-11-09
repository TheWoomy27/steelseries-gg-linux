package ggcore

import (
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

	// GameSense and overlay registries: the Windows handlers wrap each
	// result in a key named after the operation.
	for path, key := range map[string]string{"/overlays": "get_overlay_settings", "/game_integration_games": "game_integration_games",
		"/game_integration_devices": "game_integration_devices", "/known_games": "known_games"} {
		k := key
		s.Handle("GET "+path, func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, map[string]any{k: []any{}}) })
	}
	s.Handle("GET /game_integration/oled_active_for_device/{id}", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"handleGameIntegrationOledActiveCheck": false})
	})

	// Engine -> core notifications.
	for _, p := range []string{"/engine/device/connected", "/engine/device/disconnected", "/engine/device/preConnect", "/engine/device/preConnectionsComplete", "/cacheDevices"} {
		s.Handle("POST "+p, func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	}
}

// StartClient launches the Electron client (tray "Open" and /startClient):
// $GG_LINUX_CLIENT, the launcher installed beside ggd, or steelseries-gg
// from PATH (a desktop session's PATH often lacks ~/.local/bin).
func (c *Core) StartClient() {
	bin := os.Getenv("GG_LINUX_CLIENT")
	if bin == "" {
		bin = "steelseries-gg"
		if exe, err := os.Executable(); err == nil {
			if beside := filepath.Join(filepath.Dir(exe), bin); isExecutable(beside) {
				bin = beside
			}
		}
	}
	cmd := exec.Command(bin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		slog.Error("start client", "bin", bin, "err", err)
		return
	}
	go cmd.Wait()
}

func isExecutable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0
}
