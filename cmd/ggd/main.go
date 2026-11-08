// ggd is the Linux replacement for SteelSeriesGG.exe and
// SteelSeriesEngine.exe: it serves the GG core and Engine APIs that the
// Electron client talks to, drives SteelSeries hardware over hidraw, and
// hosts the GameSense API for games.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"ggl/internal/engine"
	"ggl/internal/ggcore"
	"ggl/internal/httpx"
	"ggl/internal/paths"
	"ggl/internal/prism"
	"ggl/internal/tlsutil"
)

func main() {
	verbose := flag.Bool("v", false, "verbose logging")
	flag.Parse()
	if err := run(*verbose); err != nil {
		slog.Error("ggd exiting", "err", err)
		os.Exit(1)
	}
}

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

	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	ggAddr, err := serve(ctx, core.Handler(), tlsCfg)
	if err != nil {
		return err
	}
	engAddr, err := serve(ctx, eng.Handler(), tlsCfg)
	if err != nil {
		return err
	}
	// GameSense clients talk plain HTTP to "address".
	gsAddr, err := serve(ctx, eng.GameSenseHandler(), nil)
	if err != nil {
		return err
	}
	eng.SetAddresses(engAddr, gsAddr)
	core.SetAddresses(ggAddr, engAddr)

	ps, err := prism.New(paths.PrismDB(), paths.PrismDBMig(), eng, core.Broadcast)
	if err != nil {
		return fmt.Errorf("prismsync: %w", err)
	}
	prismAddr, err := serve(ctx, ps.Handler(), tlsCfg)
	if err != nil {
		return err
	}
	core.RegisterSubApp("prismSyncV2", ggcore.SubAppMetadata{EncryptedWebServerAddress: prismAddr})

	if err := writeCoreProps(ggAddr, engAddr, gsAddr, token); err != nil {
		return err
	}
	slog.Info("ggd ready", "gg", ggAddr, "engine", engAddr, "gamesense", gsAddr)

	go core.Run(ctx)
	go eng.Run(ctx)
	if os.Getenv("GG_LINUX_NO_TRAY") == "" {
		go core.StartTray(ctx, cancel)
	}
	<-ctx.Done()
	slog.Info("ggd shutting down")
	eng.Shutdown()
	os.Remove(paths.CoreProps())
	return nil
}

func serve(ctx context.Context, h http.Handler, cfg *tls.Config) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: h, TLSConfig: cfg}
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
	return ln.Addr().String(), nil
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
		return nil, fmt.Errorf("ggd is already running")
	}
	return func() { f.Close() }, nil
}
