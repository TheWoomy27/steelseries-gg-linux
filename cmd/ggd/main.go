// ggd is the Linux replacement for SteelSeriesGG.exe and
// SteelSeriesEngine.exe: it serves the GG core and Engine APIs that the
// Electron client talks to, drives SteelSeries hardware over hidraw, and
// hosts the GameSense API for games.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"ggl/internal/engine"
	"ggl/internal/ggcore"
	"ggl/internal/httpx"
	"ggl/internal/notify"
	"ggl/internal/paths"
	"ggl/internal/prism"
	"ggl/internal/sonar"
	"ggl/internal/tlsutil"
	"ggl/internal/wineprefix"
)

func main() {
	verbose := flag.Bool("v", false, "verbose logging")
	flag.Parse()
	if err := run(*verbose); err != nil {
		if errors.Is(err, errRunning) {
			// Not a failure: the user service must not keep retrying.
			slog.Info("ggd is already running")
			return
		}
		slog.Error("ggd exiting", "err", err)
		os.Exit(1)
	}
}

var errRunning = errors.New("ggd is already running")

func run(verbose bool) error {
	if err := paths.EnsureRuntimeDirs(); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(paths.Logs(), "ggd.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if verbose || os.Getenv("GG_LINUX_DEBUG") != "" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, logFile), &slog.HandlerOptions{Level: level})))

	unlock, err := singleInstance(filepath.Join(paths.State(), "ggd.lock"))
	if err != nil {
		return err
	}
	defer unlock()

	if _, err := os.Stat(paths.DeviceSpecs()); err != nil {
		return fmt.Errorf("no imported GG data in %s; run gg-setup with your SteelSeries GG installer first", paths.Data())
	}

	cert, err := tlsutil.LoadOrCreate(paths.TLSDir())
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	token := httpx.RandomToken(32)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	eng, err := engine.New(ctx)
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	core, err := ggcore.New(ctx, token, eng)
	if err != nil {
		return fmt.Errorf("gg core: %w", err)
	}

	// GG_LINUX_PPROF=127.0.0.1:6060 serves Go's profiler, for development.
	if addr := os.Getenv("GG_LINUX_PPROF"); addr != "" {
		go http.ListenAndServe(addr, nil)
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	ggAddr, err := serve(ctx, core.Handler(), tlsCfg)
	if err != nil {
		return err
	}
	// The Engine and GameSense keep their ports across restarts where
	// they can: games in Wine prefixes read them from a copy of
	// coreProps.json there (see wineprefix), which then stays right.
	engAddr, err := serveAt(ctx, eng.Handler(), tlsCfg, "engine")
	if err != nil {
		return err
	}
	// GameSense clients talk plain HTTP to "address".
	gsAddr, err := serveAt(ctx, eng.GameSenseHandler(), nil, "gamesense")
	if err != nil {
		return err
	}
	eng.SetAddresses(engAddr, gsAddr)
	core.SetAddresses(ggAddr, engAddr)
	core.SetGameSense(eng)

	ps, err := prism.New(paths.PrismDB(), paths.PrismDBMig(), eng, core.Broadcast)
	if err != nil {
		return fmt.Errorf("prismsync: %w", err)
	}
	prismAddr, err := serve(ctx, ps.Handler(), tlsCfg)
	if err != nil {
		return err
	}
	core.RegisterSubApp("prismSyncV2", ggcore.SubAppMetadata{EncryptedWebServerAddress: prismAddr})
	go ps.Run(ctx)

	snr, err := sonar.New(token)
	if err != nil {
		return fmt.Errorf("sonar: %w", err)
	}
	// Sonar learns about headsets (ChatMix dials, wireless links) from
	// Engine, as the Windows Sonar does over GG's websocket.
	snr.SetHeadsets(headsets{eng})
	snr.OnStatus(func(status any) { core.Broadcast("EVENT_SONAR_STATUS", status) })
	eng.OnChatMix(snr.HeadsetChatMix)
	eng.OnHeadsetLink(snr.HeadsetLinkChanged)
	eng.SetAutoFlatten(func() bool {
		on, _ := core.Setting("autoFlattenEQEnabled").(bool)
		return on && core.SubAppEnabled("sonar")
	})
	// Where Windows GG shows overlays, Linux shows desktop notifications,
	// unless GG's overlay is switched off.
	core.OnNotify(func(tag, summary, body string) {
		if err := notify.Send(tag, summary, body, ""); err != nil {
			slog.Warn("notification", "tag", tag, "err", err)
		}
	})
	eng.OnBatteryLow(func(device string, percent int, sound bool) {
		if core.Setting("overlayEnabled") == false || !core.OverlayEnabled("engine", "lowBattery") {
			return
		}
		name := ""
		if sound {
			name = "battery-low"
		}
		if err := notify.Send("battery/"+device, "Low battery", fmt.Sprintf("%s: %d%% left", device, percent), name); err != nil {
			slog.Warn("low battery notification", "device", device, "err", err)
		}
	})
	// Plain HTTP, like the Windows Sonar, which registers its server URL
	// (scheme included) as webServerAddress.
	sonarAddr, err := serve(ctx, snr.Handler(), nil)
	if err != nil {
		return err
	}
	sonarMD := ggcore.SubAppMetadata{WebServerAddress: "http://" + sonarAddr}
	core.OnSubAppToggle("sonar", func(on bool) {
		if on {
			core.RegisterSubApp("sonar", sonarMD)
		}
		snr.SetActive(on)
	})
	core.RegisterSubApp("sonar", sonarMD)
	connectQuickset(core, snr)
	go snr.SetActive(core.SubAppEnabled("sonar"))

	mom, err := startMoments(ctx, core, token, tlsCfg)
	if err != nil {
		return fmt.Errorf("moments: %w", err)
	}

	if err := writeCoreProps(ggAddr, engAddr, gsAddr, token); err != nil {
		return err
	}
	slog.Info("ggd ready", "gg", ggAddr, "engine", engAddr, "gamesense", gsAddr)
	// Windows games in Wine and Proton prefixes find GameSense through a
	// copy of its coreProps.json there. A ggd run on other data (tests,
	// development) leaves the prefixes to the installed one.
	if home, err := os.UserHomeDir(); err == nil && os.Getenv("GG_LINUX_DATA") == "" {
		gs, _ := json.Marshal(map[string]any{"address": gsAddr, "encryptedAddress": engAddr})
		go (&wineprefix.Keeper{Home: home, Props: gs}).Run(ctx)
	}

	go core.Run(ctx)
	go eng.Run(ctx)
	if os.Getenv("GG_LINUX_NO_TRAY") == "" {
		go core.StartTray(ctx, cancel)
	}
	<-ctx.Done()
	slog.Info("ggd shutting down")
	snr.Stop()
	mom.Stop()
	ps.Wait()
	eng.Shutdown()
	os.Remove(paths.CoreProps())
	return nil
}

func serve(ctx context.Context, h http.Handler, cfg *tls.Config) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	return serveOn(ctx, ln, h, cfg), nil
}

// serveAt serves on the port the named service had last time if it is
// free, else on a new one it remembers.
func serveAt(ctx context.Context, h http.Handler, cfg *tls.Config, name string) (string, error) {
	file := filepath.Join(paths.State(), "ports.json")
	ports := map[string]int{}
	if b, err := os.ReadFile(file); err == nil {
		json.Unmarshal(b, &ports)
	}
	var ln net.Listener
	var err error
	if p := ports[name]; p > 0 {
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	}
	if ln == nil {
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return "", err
		}
	}
	if p := ln.Addr().(*net.TCPAddr).Port; p != ports[name] {
		ports[name] = p
		b, _ := json.Marshal(ports)
		if err := atomicWrite(file, b, 0o644); err != nil {
			slog.Warn("remember port", "service", name, "err", err)
		}
	}
	return serveOn(ctx, ln, h, cfg), nil
}

// httpErrors logs the HTTP servers' errors. Chromium rejects ggd's own
// certificate on some connections before the client's certificate check
// lets it through, and each leaves a handshake error; those are debug
// detail, not problems.
type httpErrors struct{}

func (httpErrors) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	if strings.Contains(msg, "TLS handshake error") {
		slog.Debug(msg)
	} else {
		slog.Info(msg)
	}
	return len(p), nil
}

func serveOn(ctx context.Context, ln net.Listener, h http.Handler, cfg *tls.Config) string {
	srv := &http.Server{Handler: h, TLSConfig: cfg, ErrorLog: log.New(httpErrors{}, "", 0)}
	go func() {
		var err error
		if cfg != nil {
			err = srv.ServeTLS(ln, "", "")
		} else {
			err = srv.Serve(ln)
		}
		if err != nil && err != http.ErrServerClosed {
			slog.Error("http server", "addr", ln.Addr(), "err", err)
		}
	}()
	go func() { <-ctx.Done(); srv.Close() }()
	return ln.Addr().String()
}

// writeCoreProps publishes the service addresses. The GG client reads
// coreProps.json (via $CORE_PROPS); GameSense SDK clients read the copy in
// "SteelSeries Engine 3".
func writeCoreProps(ggAddr, engAddr, gsAddr, token string) error {
	props := map[string]any{
		"address":            gsAddr,
		"encryptedAddress":   engAddr,
		"ggEncryptedAddress": ggAddr,
		"pid":                os.Getpid(),
	}
	b, _ := json.Marshal(props)
	if err := os.WriteFile(filepath.Join(paths.State(), "client-token"), []byte(token), 0o600); err != nil {
		return err
	}
	if err := atomicWrite(paths.CoreProps(), b, 0o644); err != nil {
		return err
	}
	gs, _ := json.Marshal(map[string]any{"address": gsAddr, "encryptedAddress": engAddr})
	return atomicWrite(paths.GameSenseCoreProps(), gs, 0o644)
}

// headsets gives Sonar Engine's view of the headsets.
type headsets struct{ eng *engine.Engine }

func (h headsets) Headset(usb uint32) (sonar.Headset, bool) {
	e, ok := h.eng.Headset(usb)
	return sonar.Headset{Name: e.Name, ChatMixDial: e.ChatMixDial, Wireless: e.Wireless, Connected: e.Connected}, ok
}

func (h headsets) ChatMixLevels(usb uint32) (int, int, bool) { return h.eng.ChatMixLevels(usb) }

func atomicWrite(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func singleInstance(lockPath string) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errRunning
	}
	return func() { f.Close() }, nil
}
