package ggcore

import (
	"encoding/json"
	"testing"
)

func TestOverlaySettings(t *testing.T) {
	c := testCore(t)
	// What the settings pages read: a list of overlays per product (the
	// QuickSet and Moments pages take the position from the first), and
	// each product's own switch.
	b, _ := json.Marshal(c.overlaySettings())
	var got struct {
		Settings map[string][]struct {
			Name      string
			IsEnabled bool
			Position  int
		} `json:"settings"`
		Products []struct {
			Name      string
			IsEnabled bool
		} `json:"productsettings"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"quickset", "engine", "Moments"} {
		if len(got.Settings[p]) == 0 {
			t.Errorf("no overlays for %s", p)
		}
	}
	if len(got.Products) != 3 || !got.Products[0].IsEnabled {
		t.Errorf("products: %+v", got.Products)
	}

	if !c.OverlayEnabled("quickset", "loadoutSwitchedAuto") || c.OverlayEnabled("quickset", "loadoutSwitchedManual") {
		t.Error("defaults: automatic switches on, manual ones off")
	}
	off, on, pos := 0, 1, 4
	c.setOverlay("quickset", "loadoutSwitchedManual", &on, nil)
	c.setOverlay("quickset", "", nil, &pos)
	if !c.OverlayEnabled("quickset", "loadoutSwitchedManual") {
		t.Error("overlay not switched on")
	}
	c.setOverlay("quickset", "", &off, nil)
	if c.OverlayEnabled("quickset", "loadoutSwitchedManual") || c.OverlayEnabled("quickset", "loadoutSwitchedAuto") {
		t.Error("a product switched off still shows its overlays")
	}
	if !c.OverlayEnabled("engine", "lowBattery") {
		t.Error("switching QuickSet off switched the Engine's off")
	}
	b, _ = json.Marshal(c.overlaySettings())
	json.Unmarshal(b, &got)
	if got.Settings["quickset"][0].Position != 4 || got.Products[0].IsEnabled {
		t.Errorf("after changes: %+v %+v", got.Settings["quickset"][0], got.Products[0])
	}
}

// With no games installed the client still needs a list: it takes null as
// it comes and fails on it.
func TestScanGamesEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	c := testCore(t)
	b, _ := json.Marshal(c.scanGames())
	if string(b) != "[]" {
		t.Errorf("no games: %s, want []", b)
	}
}
