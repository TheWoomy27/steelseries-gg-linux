package ggcore

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"ggl/internal/httpx"
	"ggl/internal/paths"
)

// importedVersion is the GG version gg-setup imported.
func importedVersion() string {
	var info struct {
		Version string `json:"ggVersion"`
	}
	if b, err := os.ReadFile(filepath.Join(paths.Data(), "import.json")); err == nil {
		json.Unmarshal(b, &info)
	}
	return info.Version
}

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
		httpx.JSON(w, map[string]any{"applicationMetadata": map[string]any{"ggVersion": importedVersion()}})
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

	// The Windows handlers wrap each result in a key named after the
	// operation.
	s.Handle("GET /known_games", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"known_games": []any{}})
	})
	// GameSense: the games lighting devices and their handlers.
	s.Handle("GET /game_integration_games", func(w http.ResponseWriter, r *http.Request) {
		var games any = []any{}
		if gs := c.gameSenseUI(); gs != nil {
			games = gs.GameIntegrationGames()
		}
		httpx.JSON(w, map[string]any{"game_integration_games": games})
	})
	s.Handle("GET /game_integration_devices", func(w http.ResponseWriter, r *http.Request) {
		var devs any = []any{}
		if gs := c.gameSenseUI(); gs != nil {
			devs = gs.GameIntegrationDevices()
		}
		httpx.JSON(w, map[string]any{"game_integration_devices": devs})
	})
	s.Handle("POST /game_integration_toggle", func(w http.ResponseWriter, r *http.Request) {
		p, err := decode[struct {
			Game    string `json:"game"`
			Enabled bool   `json:"enabled"`
		}](r)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if gs := c.gameSenseUI(); gs != nil {
			gs.ToggleGame(p.Game, p.Enabled)
		}
		httpx.OK(w)
	})
	s.Handle("POST /set_user_handlers", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if gs := c.gameSenseUI(); gs != nil {
			if err := gs.SetUserHandlers(body); err != nil {
				httpx.Error(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		httpx.OK(w)
	})
	// Whether a GameSense app has a device's screen (GG says so on the
	// device's screen settings).
	s.Handle("GET /game_integration/oled_active_for_device/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		active, app := false, ""
		if gs := c.gameSenseUI(); gs != nil {
			active, app = gs.OLEDActive(id)
		}
		httpx.JSON(w, map[string]any{"handleGameIntegrationOledActiveCheck": map[string]any{"active": active, "appName": app}})
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
	// The client's own output (Electron and Chromium messages) goes to
	// Logs/client.log, started afresh when it grows past 5 MB.
	logPath := filepath.Join(paths.Logs(), "client.log")
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if st, err := os.Stat(logPath); err == nil && st.Size() > 5<<20 {
		flags |= os.O_TRUNC
	}
	if f, err := os.OpenFile(logPath, flags, 0o644); err == nil {
		fmt.Fprintf(f, "\n--- client started %s\n", time.Now().Format(time.RFC3339))
		cmd.Stdout, cmd.Stderr = f, f
		defer f.Close()
	}
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

// GameSenseUI is what GG's GameSense pages need from the Engine.
type GameSenseUI interface {
	GameIntegrationGames() any
	GameIntegrationDevices() any
	ToggleGame(game string, on bool)
	SetUserHandlers(body []byte) error
	OLEDActive(deviceID int) (bool, string)
}

// SetGameSense connects GG's GameSense pages to the Engine.
func (c *Core) SetGameSense(g GameSenseUI) {
	c.mu.Lock()
	c.gameSense = g
	c.mu.Unlock()
}

func (c *Core) gameSenseUI() GameSenseUI {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gameSense
}
