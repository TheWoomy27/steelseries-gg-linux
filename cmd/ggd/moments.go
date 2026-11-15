package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"ggl/internal/ggcore"
	"ggl/internal/moments"
	"ggl/internal/notify"
	"ggl/internal/paths"
)

// startMoments runs Moments, GG's clip recorder, as the "moments" sub-app.
// It records the game being played, as ggcore's game watcher sees it.
func startMoments(ctx context.Context, core *ggcore.Core, token string, tlsCfg *tls.Config, keys *globalShortcuts) (*moments.Service, error) {
	mom, err := moments.New(token, moments.Paths{
		DB:     filepath.Join(paths.State(), "moments.db"),
		Data:   filepath.Join(paths.Data(), "moments"),
		Sounds: filepath.Join(paths.Assets(), "moments", "sounds"),
	}, momentsHost{core})
	if err != nil {
		return nil, err
	}
	addr, err := serve(ctx, mom.Handler(), tlsCfg)
	if err != nil {
		mom.Stop()
		return nil, err
	}
	md := ggcore.SubAppMetadata{EncryptedWebServerAddress: addr}
	core.OnSubAppToggle("moments", func(on bool) {
		if on {
			core.RegisterSubApp("moments", md)
		}
		mom.SetActive(on)
	})
	core.RegisterSubApp("moments", md)
	core.WatchGames(mom.Capturing, func(g *ggcore.ScannedGame) {
		if g == nil {
			mom.GameChanged(nil)
			return
		}
		mom.GameChanged(&moments.LibraryGame{UID: g.UniqueIdentifier, Name: g.Name, Exe: g.ExePath, Dir: g.Directory, Scanner: g.ScannerID})
	})
	go mom.SetActive(core.SubAppEnabled("moments"))
	keys.setMoments(mom)
	slog.Info("moments ready", "addr", addr)
	return mom, nil
}

// ggctlPath is the ggctl installed beside ggd.
func ggctlPath() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "ggctl")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("ggctl"); err == nil {
		return p
	}
	return "ggctl"
}

type momentsHost struct{ core *ggcore.Core }

func (h momentsHost) Games() []moments.LibraryGame {
	var out []moments.LibraryGame
	for _, g := range h.core.ScannedGames() {
		if g.Uninstalled {
			continue
		}
		out = append(out, moments.LibraryGame{UID: g.UniqueIdentifier, Name: g.Name, Exe: g.ExePath, Dir: g.Directory, Scanner: g.ScannerID})
	}
	return out
}

func (h momentsHost) SetRecording(on bool) { h.core.SetRecording(on) }

func (h momentsHost) Notify(overlay, tag, summary, body string) {
	if overlay != "" && !h.core.OverlayEnabled("Moments", overlay) {
		return
	}
	if err := notify.Send(tag, summary, body, ""); err != nil {
		slog.Warn("moments notification", "err", fmt.Sprint(err))
	}
}
