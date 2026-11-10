package engine

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ggl/internal/devicert"
	"ggl/internal/gamesense"
	"ggl/internal/oled"
	"ggl/internal/paths"
)

// Device screens (the Apex OLED): GameSense games, and the Engine's own
// apps (see oledapps.go), show text and pictures on them. The Engine
// draws each screen (package oled) and writes it with the device's
// gamesense_oled_display command; when nothing is showing it hands the
// screen back to the device's own picture (disable_gamesense_oled).

type oledState struct {
	mu      sync.Mutex
	showing map[int]string // game on each device's screen, by Engine id
	present bool           // a device with a screen is connected
}

// oledDevice is a connected device with a GameSense screen.
type oledDevice struct {
	id   int
	name string
	decl *devicert.Decl
	gs   gamesense.Device
}

// oledDevices lists the connected devices whose spec can show GameSense
// screens.
func (e *Engine) oledDevices() []oledDevice {
	e.mu.Lock()
	lives := make([]*liveDevice, 0, len(e.live))
	for _, l := range e.live {
		lives = append(lives, l)
	}
	e.mu.Unlock()
	var out []oledDevice
	for _, l := range lives {
		l.mu.Lock()
		ready := l.ready
		l.mu.Unlock()
		d := l.primary()
		if !ready || d == nil || d.Api("gamesense_oled_display") == nil || d.Api("disable_gamesense_oled") == nil {
			continue
		}
		info := e.gameSenseInfo(d)
		if info == nil {
			continue
		}
		dev := gamesense.Device{ProductID: d.ID, Types: info.Types, Zones: info.Zones}
		for _, t := range info.Types {
			var w, h int
			if n, _ := fmt.Sscanf(t, "screened-%dx%d", &w, &h); n == 2 {
				dev.ScreenW, dev.ScreenH = w, h
			}
		}
		if dev.ScreenW == 0 {
			continue
		}
		out = append(out, oledDevice{id: l.dbID, name: l.cat.Name, decl: d, gs: dev})
	}
	return out
}

// runOLED keeps the screens showing what GameSense says.
func (e *Engine) runOLED(ctx context.Context) {
	oled.SetIconFile(filepath.Join(paths.EngineRoot(), "game-integration", "lib", "screener.lsp"))
	type devState struct {
		last    []byte
		written time.Time
		showing bool
		failed  time.Time
	}
	states := map[int]*devState{}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			for _, d := range e.oledDevices() {
				if st := states[d.id]; st != nil && st.showing {
					d.decl.WriteApi("disable_gamesense_oled", map[string]any{})
				}
			}
			return
		case <-tick.C:
		}
		devs := e.oledDevices()
		showing := map[int]string{}
		for _, d := range devs {
			st := states[d.id]
			if st == nil {
				st = &devState{}
				states[d.id] = st
			}
			if time.Since(st.failed) < 5*time.Second {
				continue
			}
			sc := e.gameSense.state.Screen(&d.gs)
			if sc == nil {
				if st.showing {
					if err := d.decl.WriteApi("disable_gamesense_oled", map[string]any{}); err != nil {
						slog.Warn("oled: back to the device's picture", "device", d.name, "err", err)
						st.failed = time.Now()
						continue
					}
					slog.Info("oled: device's own picture", "device", d.name)
					st.showing, st.last = false, nil
				}
				continue
			}
			showing[d.id] = sc.Game
			pic := sc.Image
			if pic == nil {
				pic = oled.Render(d.gs.ScreenW, d.gs.ScreenH, oled.Icon(sc.IconID), sc.Lines).Pack()
			}
			// Unchanged pictures are written again now and then, in case
			// the device reset its screen.
			if bytes.Equal(pic, st.last) && time.Since(st.written) < 10*time.Second {
				continue
			}
			if err := d.decl.WriteApi("gamesense_oled_display", map[string]any{"data": pic}); err != nil {
				slog.Warn("oled: write", "device", d.name, "err", err)
				st.failed = time.Now()
				continue
			}
			if !st.showing {
				slog.Info("oled: showing", "device", d.name, "game", sc.Game)
			}
			st.last, st.written, st.showing = pic, time.Now(), true
		}
		for id := range states {
			found := false
			for _, d := range devs {
				found = found || d.id == id
			}
			if !found {
				delete(states, id)
			}
		}
		e.oled.mu.Lock()
		e.oled.showing = showing
		e.oled.present = len(devs) > 0
		e.oled.mu.Unlock()
	}
}

// OLEDActive reports whether GameSense controls a device's screen, and
// for which app (GG shows it on the device's screen settings).
func (e *Engine) OLEDActive(deviceID int) (bool, string) {
	e.oled.mu.Lock()
	game := e.oled.showing[deviceID]
	e.oled.mu.Unlock()
	if game == "" {
		return false, ""
	}
	for _, g := range e.gameSense.state.Games() {
		if g.Name == game && g.DisplayName != "" {
			return true, g.DisplayName
		}
	}
	return true, strings.ToUpper(game)
}

func (e *Engine) oledPresent() bool {
	e.oled.mu.Lock()
	defer e.oled.mu.Unlock()
	return e.oled.present
}
