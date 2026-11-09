package ggcore

import (
	"path/filepath"
	"testing"

	"ggl/internal/events"
	"ggl/internal/store"
)

func testCore(t *testing.T) *Core {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "gg.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &Core{db: db, sock: events.NewHub("test", "t"), evts: events.NewHub("test", "t")}
}

func TestProfileFromSonarPreset(t *testing.T) {
	c := testCore(t)
	c.AddDefaultConfig(LoadoutConfig{ConfigType: "sonar", ConfigID: "flat"})
	c.AddDefaultConfig(LoadoutConfig{ConfigType: "sonar", ConfigID: "other"}) // Default keeps its first
	c.withLoadouts(func(st *loadoutState) (any, int) {
		cachePresets(st, map[string]any{"appName": "sonar", "presetList": map[string]any{"presets": []any{
			map[string]any{"id": "fortnite", "name": "Fortnite Pro Preset", "game_ids": []any{"9_Fortnite", " 1_123"}},
		}}})
		return nil, 200
	})
	if st := c.loadLoadouts(); st.ProfilesEnabled {
		t.Error("profiles start on")
	}

	fortnite := ScannedGame{ID: "epic-Fortnite", Name: "Fortnite", UniqueIdentifier: "9_Fortnite"}
	id := c.loadoutForGame(fortnite)
	if id == "" {
		t.Fatal("no profile made from the Fortnite preset")
	}
	if again := c.loadoutForGame(fortnite); again != id {
		t.Errorf("a second launch made another profile: %s, %s", id, again)
	}
	if c.loadoutForGame(ScannedGame{UniqueIdentifier: "1_123", Name: "Spaced"}) == "" {
		t.Error("a game id listed with a leading space was not matched")
	}
	if c.loadoutForGame(ScannedGame{UniqueIdentifier: "1_999", Name: "Other"}) != "" {
		t.Error("a game without a preset got a profile")
	}

	st := c.loadLoadouts()
	l := findLoadout(&st, id)
	if l.Name != "Fortnite" || len(l.Bindings) != 1 || l.Bindings[0].Value != "9_Fortnite" {
		t.Errorf("profile %+v", l)
	}
	if len(l.Configs) != 1 || l.Configs[0] != (LoadoutConfig{ConfigType: "sonar", ConfigID: "fortnite"}) {
		t.Errorf("profile configs %+v, want the Fortnite preset", l.Configs)
	}
	if d := findLoadout(&st, defaultLoadoutID); len(d.Configs) != 1 || d.Configs[0].ConfigID != "flat" {
		t.Errorf("Default configs %+v", d.Configs)
	}

	// Off, nothing is made.
	c.setSetting("autoApplyQuicksetSonarPresets", false)
	if c.loadoutForGame(ScannedGame{UniqueIdentifier: "9_Fortnite_Studio", Name: "UEFN"}) != "" {
		t.Error("a profile was made with auto-apply off")
	}
}

func TestActiveProfileConfigs(t *testing.T) {
	c := testCore(t)
	c.AddDefaultConfig(LoadoutConfig{ConfigType: "sonar", ConfigID: "flat"})
	c.SetActiveConfig(LoadoutConfig{ConfigType: "sonar", ConfigID: "bass"})
	c.SetActiveConfig(LoadoutConfig{ConfigType: "engine", ConfigMetadata: "159", ConfigID: "apex-red"})
	st := c.loadLoadouts()
	d := findLoadout(&st, defaultLoadoutID)
	want := []LoadoutConfig{{ConfigType: "sonar", ConfigID: "bass"}, {ConfigType: "engine", ConfigMetadata: "159", ConfigID: "apex-red"}}
	if len(d.Configs) != 2 || d.Configs[0] != want[0] || d.Configs[1] != want[1] {
		t.Errorf("Default configs %+v, want %+v", d.Configs, want)
	}
	c.ReplaceConfig("sonar", "bass", "flat")
	st = c.loadLoadouts()
	if d := findLoadout(&st, defaultLoadoutID); d.Configs[0].ConfigID != "flat" {
		t.Errorf("deleted preset not replaced: %+v", d.Configs)
	}
}

func TestGameUID(t *testing.T) {
	if got := gameUID(scannerSteam, "1172470"); got != "1_1172470" {
		t.Errorf("Steam: %s", got)
	}
	if got := gameUID(scannerEpic, "Fortnite"); got != "9_Fortnite" {
		t.Errorf("Epic: %s", got)
	}
}
