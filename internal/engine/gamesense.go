package engine

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"ggl/internal/httpx"
)

// gameSense implements the public GameSense SDK endpoints games call.
// Registrations and bindings are recorded so the client's GameSense pages
// list the game; device-side effects use the same handlers as Windows
// when the bound device supports them.
type gameSense struct {
	e     *Engine
	mu    sync.Mutex
	games map[string]*gsGame
}

type gsGame struct {
	Game        string                     `json:"game"`
	DisplayName string                     `json:"game_display_name"`
	Developer   string                     `json:"developer"`
	Events      map[string]json.RawMessage `json:"events"`
	Handlers    map[string]json.RawMessage `json:"handlers"`
	LastEvent   map[string]any             `json:"last_event"`
	LastSeen    time.Time                  `json:"last_seen"`
}

func newGameSense(e *Engine) *gameSense { return &gameSense{e: e, games: map[string]*gsGame{}} }

func (g *gameSense) game(name string) *gsGame {
	name = strings.ToUpper(name)
	gm, ok := g.games[name]
	if !ok {
		gm = &gsGame{Game: name, Events: map[string]json.RawMessage{}, Handlers: map[string]json.RawMessage{}}
		g.games[name] = gm
	}
	gm.LastSeen = time.Now()
	return gm
}

func (g *gameSense) handle(w http.ResponseWriter, r *http.Request, path string) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var p map[string]any
	if err := json.Unmarshal(body, &p); err != nil {
		httpx.Error(w, 400, "bad json")
		return
	}
	name, _ := p["game"].(string)
	if name == "" {
		httpx.Error(w, 400, "missing game")
		return
	}
	g.mu.Lock()
	gm := g.game(name)
	event, _ := p["event"].(string)
	switch path {
	case "/game_metadata":
		gm.DisplayName, _ = p["game_display_name"].(string)
		gm.Developer, _ = p["developer"].(string)
	case "/register_game_event", "/bind_game_event":
		gm.Events[event] = body
		if path == "/bind_game_event" {
			gm.Handlers[event] = body
		}
	case "/remove_game_event":
		delete(gm.Events, event)
		delete(gm.Handlers, event)
	case "/remove_game":
		delete(g.games, strings.ToUpper(name))
	case "/game_event":
		gm.LastEvent = p
	case "/multiple_game_events":
		if evs, ok := p["events"].([]any); ok && len(evs) > 0 {
			gm.LastEvent, _ = evs[len(evs)-1].(map[string]any)
		}
	}
	g.mu.Unlock()
	if path == "/game_metadata" || path == "/register_game_event" || path == "/bind_game_event" || path == "/remove_game" {
		g.e.emit("EVENT_GAMESENSE_NEW_DATA", map[string]any{"game": strings.ToUpper(name)})
	}
	httpx.OK(w)
}
