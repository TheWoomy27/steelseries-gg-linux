package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"

	"ggl/internal/ggcore"
	"ggl/internal/hotkeys"
	"ggl/internal/moments"
)

// The Moments shortcuts: their portal id, what ggctl does for each, and
// their description.
var momentsShortcuts = map[int][3]string{
	ggcore.ShortcutMomentsClip:    {"moments-clip", "moments clip", "Moments: save a clip"},
	ggcore.ShortcutMomentsMicMute: {"moments-mic", "moments mic", "Moments: mute the microphone"},
	ggcore.ShortcutMomentsDesktop: {"moments-desktop", "moments desktop", "Moments: desktop capture"},
	ggcore.ShortcutMomentsEnable:  {"moments-toggle", "moments toggle", "Moments: capture on or off"},
}

// globalShortcuts makes GG's shortcuts work across the desktop: the
// Moments ones while capture is on, and QuickSet's profile shortcuts
// while profiles are on and the shortcut has a profile. They are bound
// only then, so the keys stay the applications' own otherwise.
type globalShortcuts struct {
	core  *ggcore.Core
	keys  hotkeys.Binder // nil: the session has no global shortcuts
	ggctl string         // quoted for the shell the commands run in

	mu   sync.Mutex
	mom  *moments.Service
	last map[string]hotkeys.Bind // the binds last set
}

func newGlobalShortcuts(ctx context.Context, core *ggcore.Core) *globalShortcuts {
	g := &globalShortcuts{core: core, keys: hotkeys.New(ctx), ggctl: shellQuote(ggctlPath())}
	if g.keys == nil {
		slog.Info("no global shortcuts in this session; bind keys to `ggctl moments clip` and `ggctl profile N` instead")
		return g
	}
	core.OnShortcutsChanged(g.apply)
	go g.apply()
	return g
}

// setMoments adds Moments' shortcuts.
func (g *globalShortcuts) setMoments(mom *moments.Service) {
	g.mu.Lock()
	g.mom = mom
	g.mu.Unlock()
	mom.OnCaptureChanged(g.apply)
	go g.apply()
}

// apply binds the shortcuts that should be bound now.
func (g *globalShortcuts) apply() {
	if g.keys == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	binds := map[string]hotkeys.Bind{}
	add := func(id int, b hotkeys.Bind) {
		t, ok := g.core.ShortcutTrigger(id)
		if !ok {
			return
		}
		spec, ok := hotkeys.Spec(t.Modifiers, t.HIDCode)
		if !ok {
			slog.Warn("hotkeys: key can't be bound", "shortcut", b.Description, "hid", t.HIDCode, "modifiers", t.Modifiers)
			return
		}
		if other, taken := binds[spec]; taken {
			slog.Warn("hotkeys: two shortcuts on one key; keeping the first", "key", spec, "kept", other.Description, "dropped", b.Description)
			return
		}
		binds[spec] = b
	}
	if g.mom != nil && g.mom.Capturing() {
		for _, id := range []int{ggcore.ShortcutMomentsClip, ggcore.ShortcutMomentsMicMute, ggcore.ShortcutMomentsDesktop, ggcore.ShortcutMomentsEnable} {
			sc := momentsShortcuts[id]
			add(id, hotkeys.Bind{ID: sc[0], Command: g.ggctl + " " + sc[1], Description: sc[2]})
		}
	}
	profiles := g.core.ProfileShortcuts()
	for idx := 1; idx <= 5; idx++ {
		if _, ok := profiles[idx]; ok {
			add(ggcore.ShortcutQuicksetProfile+idx, hotkeys.Bind{
				ID:          fmt.Sprintf("quickset-profile-%d", idx),
				Command:     fmt.Sprintf("%s profile %d", g.ggctl, idx),
				Description: fmt.Sprintf("QuickSet: switch to profile %d", idx),
			})
		}
	}
	// Profile changes call this too; an unchanged set is left alone.
	if g.last != nil && maps.Equal(binds, g.last) {
		return
	}
	g.last = binds
	g.keys.Set(binds)
}

// shellQuote quotes s for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
