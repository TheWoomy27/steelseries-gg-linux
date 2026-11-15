package prism

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"ggl/internal/wldpms"
)

// Prism dims the lighting (or turns it off) while the monitors sleep, as
// each configuration's dimFactorOnMonitorSleep says. Hyprland tells
// whether its monitors are powered (DPMS) when asked; KWin (KDE Plasma)
// tells each change through its org_kde_kwin_dpms protocol. Elsewhere
// the monitors count as awake.

// sleepWatcher tells whether every monitor is off.
type sleepWatcher interface {
	asleep() bool
}

var (
	sleepOnce sync.Once
	sleepSrc  sleepWatcher
)

// newSleepWatcher picks how to learn the monitors' power; tests replace it.
var newSleepWatcher = func() sleepWatcher {
	switch {
	case os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "":
		return &hyprlandSleep{}
	case os.Getenv("WAYLAND_DISPLAY") != "":
		k := &kwinSleep{}
		go k.run(context.Background())
		return k
	}
	return noSleep{}
}

// monitorsAsleep reports whether every monitor is off. The watching
// starts the first time a configuration needs it.
func monitorsAsleep() bool {
	sleepOnce.Do(func() { sleepSrc = newSleepWatcher() })
	return sleepSrc.asleep()
}

type noSleep struct{}

func (noSleep) asleep() bool { return false }

// hyprlandSleep asks Hyprland, at most every few seconds.
type hyprlandSleep struct {
	sync.Mutex
	checked time.Time
	state   bool
}

func (h *hyprlandSleep) asleep() bool {
	h.Lock()
	defer h.Unlock()
	if time.Since(h.checked) < 3*time.Second {
		return h.state
	}
	h.checked = time.Now()
	h.state = false
	out, err := exec.Command("hyprctl", "-j", "monitors").Output()
	if err != nil {
		return false
	}
	var ms []struct {
		DPMS bool `json:"dpmsStatus"`
	}
	if json.Unmarshal(out, &ms) != nil || len(ms) == 0 {
		return false
	}
	for _, m := range ms {
		if m.DPMS {
			return false
		}
	}
	h.state = true
	return true
}

// kwinSleep follows KWin's power events. If the connection drops (KWin
// restarted) it reconnects; the monitors count as awake meanwhile.
type kwinSleep struct {
	state atomic.Bool
}

func (k *kwinSleep) asleep() bool { return k.state.Load() }

// kwinRetry is how long kwinSleep waits before reconnecting.
var kwinRetry = 5 * time.Second

func (k *kwinSleep) run(ctx context.Context) {
	path, err := wldpms.SocketPath()
	if err != nil {
		return
	}
	warned := false
	for {
		err := wldpms.Watch(ctx, path, func(asleep bool) {
			slog.Info("prism: monitors", "asleep", asleep)
			k.state.Store(asleep)
		})
		k.state.Store(false)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, wldpms.ErrUnsupported) {
			slog.Info("prism: the compositor does not tell when the monitors sleep; lighting stays on")
			return
		}
		if !warned {
			slog.Warn("prism: following the monitors' power", "err", err)
			warned = true
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(kwinRetry):
		}
	}
}

// usesDimming reports whether a layer dims anything while the monitors
// sleep (so whether they need watching).
func usesDimming(g *Group) bool {
	if g.GlobalConfig.DimFactorOnMonitorSleep > 0 {
		return true
	}
	for i := range g.DeviceTypeConfigs {
		if g.DeviceTypeConfigs[i].Config.DimFactorOnMonitorSleep > 0 {
			return true
		}
	}
	for i := range g.PerKeyConfigs {
		if g.PerKeyConfigs[i].Config.DimFactorOnMonitorSleep > 0 {
			return true
		}
	}
	for i := range g.DeviceConfigs {
		if g.DeviceConfigs[i].Config.DimFactorOnMonitorSleep > 0 {
			return true
		}
	}
	return false
}
