package engine

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ggl/internal/events"
	"ggl/internal/store"
)

func gameSenseEngine(t *testing.T, path string) *Engine {
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{state: st, broadcast: func(string, any) {}, hub: events.NewHub("test", "")}
	e.gameSense = newGameSense(e)
	return e
}

func gsPost(e *Engine, path, body string) {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	e.gameSense.handle(httptest.NewRecorder(), r, path)
}

// A game removed from GameSense stays removed after a restart, even when
// the user had set their own handlers for its events.
func TestRemovedGameStaysRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.json")
	e := gameSenseEngine(t, path)
	gsPost(e, "/bind_game_event", `{"game":"OLDGAME","event":"HEALTH","handlers":[{"device-type":"keyboard","zone":"function-keys","color":{"red":255,"green":0,"blue":0}}]}`)
	if err := e.SetUserHandlers([]byte(`{"game":"OLDGAME","product_id":5650,"events":[{"event":"HEALTH",
		"illumination_handler":{"handler":"{\"device-type\":\"keyboard\",\"zone\":\"escape\",\"color\":{\"red\":0,\"green\":0,\"blue\":255}}"},
		"screen_handler":{"handler":"[{\"device-type\":\"screened\",\"zone\":\"one\",\"datas\":[{\"has-text\":true}]}]","default_handler":"{}"}}]}`)); err != nil {
		t.Fatal(err)
	}
	gsPost(e, "/remove_game", `{"game":"OLDGAME"}`)
	if len(e.gameSense.state.Games()) != 0 {
		t.Fatalf("removed game still listed: %+v", e.gameSense.state.Games())
	}

	restarted := gameSenseEngine(t, path)
	if games := restarted.gameSense.state.Games(); len(games) != 0 {
		t.Errorf("after a restart the removed game is back: %+v", games)
	}
}

// So does an event removed from a game.
func TestRemovedEventStaysRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.json")
	e := gameSenseEngine(t, path)
	gsPost(e, "/bind_game_event", `{"game":"G","event":"AMMO","handlers":[{"device-type":"keyboard","zone":"escape","color":{"red":255,"green":0,"blue":0}}]}`)
	gsPost(e, "/bind_game_event", `{"game":"G","event":"HEALTH","handlers":[{"device-type":"keyboard","zone":"escape","color":{"red":255,"green":0,"blue":0}}]}`)
	if err := e.SetUserHandlers([]byte(`{"game":"G","product_id":5650,"events":[{"event":"AMMO",
		"illumination_handler":{"handler":"{\"device-type\":\"keyboard\",\"zone\":\"escape\",\"color\":{\"red\":0,\"green\":0,\"blue\":255}}"}}]}`)); err != nil {
		t.Fatal(err)
	}
	gsPost(e, "/remove_game_event", `{"game":"G","event":"AMMO"}`)

	restarted := gameSenseEngine(t, path)
	games := restarted.gameSense.state.Games()
	if len(games) != 1 || strings.Join(games[0].Events, ",") != "HEALTH" {
		t.Errorf("after a restart: %+v, want G with HEALTH only", games)
	}
}
