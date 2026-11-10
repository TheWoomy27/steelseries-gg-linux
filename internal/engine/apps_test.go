package engine

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"ggl/internal/events"
	"ggl/internal/gamesense"
	"ggl/internal/store"

	_ "modernc.org/sqlite"
)

// appsEngine is an Engine with GameSense, ImageSync's table and a state
// store.
func appsEngine(t *testing.T) *Engine {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE image_sync(enabled INTEGER NOT NULL, gif_data TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "engine.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{db: db, state: st, broadcast: func(string, any) {}, hub: events.NewHub("test", "")}
	e.gameSense = &gameSense{e: e, state: gamesense.New()}
	e.imageSync = &imageSync{e: e}
	return e
}

func TestToggleEngineApps(t *testing.T) {
	e := appsEngine(t)
	if !e.PrismLightingEnabled() {
		t.Fatal("Prism lighting starts off")
	}
	e.toggleEngineApps(`{"on":[{"gamesense_game_name":"","is_image_sync":true}],"off":[{"gamesense_game_name":"CSGO"}],"toggle":[{"is_prism_sync":true}]}`)
	if on, _ := e.imageSyncEnabled(); !on {
		t.Error("ImageSync not turned on")
	}
	if e.gameSense.state.Enabled("CSGO") {
		t.Error("the game not turned off")
	}
	if e.PrismLightingEnabled() {
		t.Error("PrismSync not toggled off")
	}
	e.toggleEngineApps(`{"on":[],"off":[{"is_image_sync":true}],"toggle":[{"is_prism_sync":true},{"gamesense_game_name":"CSGO"}]}`)
	if on, _ := e.imageSyncEnabled(); on {
		t.Error("ImageSync not turned off")
	}
	if !e.PrismLightingEnabled() || !e.gameSense.state.Enabled("CSGO") {
		t.Error("toggles not undone")
	}
	var disabled []string
	e.state.Get("gameSenseDisabled", &disabled)
	if len(disabled) != 0 {
		t.Errorf("stored disabled games %v", disabled)
	}
}

func TestGameEventBinding(t *testing.T) {
	e := appsEngine(t)
	gs := e.gameSense.state
	gs.Bind("APP", "MUTE", gamesense.EventInfo{Max: 1}, []json.RawMessage{json.RawMessage(`{"device-type":"rgb-3-zone","zone":"one","color":{"red":255,"green":0,"blue":0}}`)})
	e.gameEvent(`{"game":"app","event":"mute","value":1}`)
	dev := &gamesense.Device{ProductID: 1, Types: []string{"rgb-3-zone"}, Zones: map[string][]int{"one": {0}, "all": {0, 1, 2}}}
	if got := gs.Lights(dev); got[0] != (gamesense.Color{R: 255}) {
		t.Errorf("after the binding: %v", got)
	}
}

func TestBitmap22x6(t *testing.T) {
	full := make([][3]int, 132)
	for i := range full {
		full[i] = [3]int{i, 0, 0}
	}
	if got := bitmap22x6(full); got[23] != [3]int{23, 0, 0} || got[131] != [3]int{131, 0, 0} {
		t.Error("a 22x6 frame moved")
	}
	// A TKL's 18-wide frame keeps its rows.
	tkl := make([][3]int, 18*6)
	for i := range tkl {
		tkl[i] = [3]int{i, 1, 0}
	}
	got := bitmap22x6(tkl)
	if got[22] != [3]int{18, 1, 0} || got[22+17] != [3]int{35, 1, 0} || got[22+18] != [3]int{} {
		t.Errorf("TKL rows: %v %v %v", got[22], got[22+17], got[22+18])
	}
}
