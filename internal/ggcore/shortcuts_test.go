package ggcore

import (
	"os"
	"testing"

	"ggl/internal/paths"
)

// TestShortcutDefaults replays the shortcut statements of GG's own
// migrations (from the imported GG data) and checks the Moments defaults.
func TestShortcutDefaults(t *testing.T) {
	if _, err := os.Stat(paths.GGDBMig()); err != nil {
		t.Skip("no imported GG data")
	}
	all, err := shortcutDefaults(paths.GGDBMig())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int]Shortcut{}
	for _, s := range all {
		byID[s.ID] = s
	}
	if got := byID[ShortcutMomentsClip].TriggerData; got != `{"hidCode":22,"page":1,"type":2,"direction":1,"modifiers":4}` {
		t.Errorf("clip shortcut = %s", got)
	}
	if got := byID[ShortcutMomentsEnable].TriggerData; got != `{"page":1,"type":2,"direction":1}` {
		t.Errorf("enable shortcut = %s, want none (a later migration clears it)", got)
	}
	t.Logf("%d shortcuts", len(all))
}
