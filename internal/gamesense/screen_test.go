package gamesense

import (
	"encoding/json"
	"testing"
	"time"
)

func apex() *Device {
	d := keyboard()
	d.Types = append(d.Types, "screened", "screened-128x40", "screened-2-lines")
	d.ScreenW, d.ScreenH = 128, 40
	return d
}

func TestScreenText(t *testing.T) {
	s, _ := newState()
	bind(t, s, "HEALTH", `{"device-type":"screened","zone":"one","mode":"screen","datas":[{"lines":[{"has-text":true,"prefix":"HP ","suffix":"%","bold":true},{"has-progress-bar":true}],"icon-id":1}]}`)
	if s.Screen(apex()) != nil {
		t.Fatal("a screen before any event")
	}
	value(s, "HEALTH", 72)
	sc := s.Screen(apex())
	if sc == nil || sc.Game != "GAME" || sc.IconID != 1 || len(sc.Lines) != 2 {
		t.Fatalf("screen: %+v", sc)
	}
	if sc.Lines[0].Text != "HP 72%" || !sc.Lines[0].Bold || !sc.Lines[1].Bar || sc.Lines[1].Value != 72 {
		t.Errorf("lines: %+v", sc.Lines)
	}
	// A keyboard without a screen shows nothing.
	if s.Screen(keyboard()) != nil {
		t.Error("screen on a device without one")
	}
}

func TestScreenContextAndTiming(t *testing.T) {
	s, c := newState()
	// A song for 2 s, then its artist for 1 s, twice; then the device's picture.
	s.Bind("GAME", "SONG", EventInfo{Max: 100, ValueOptional: true}, []json.RawMessage{json.RawMessage(
		`{"device-type":"screened-128x40","mode":"screen","datas":[{"has-text":true,"context-frame-key":"title","length-millis":2000},{"has-text":true,"context-frame-key":"artist","length-millis":1000,"repeats":2}]}`)})
	start := c.t
	s.Event("GAME", "SONG", json.RawMessage(`{"value":0,"frame":{"title":"Song","artist":"Band"}}`))
	for _, step := range []struct {
		ms   int
		want string
	}{{0, "Song"}, {1999, "Song"}, {2000, "Band"}, {3000, "Song"}, {5500, "Band"}, {6000, ""}} {
		c.t = start.Add(time.Duration(step.ms) * time.Millisecond)
		sc := s.Screen(apex())
		got := ""
		if sc != nil {
			got = sc.Lines[0].Text
		}
		if got != step.want {
			t.Errorf("at %d ms: %q, want %q", step.ms, got, step.want)
		}
	}
}

func TestScreenRangesAndPriority(t *testing.T) {
	s, _ := newState()
	bind(t, s, "AMMO", `{"device-type":"screened","mode":"screen","datas":[{"low":0,"high":0,"datas":[{"has-text":true,"arg":"EMPTY"}]},{"low":1,"high":100,"datas":[{"has-text":true,"prefix":"Ammo "}]}]}`)
	value(s, "AMMO", 0)
	if sc := s.Screen(apex()); sc == nil || sc.Lines[0].Text != "EMPTY" {
		t.Fatalf("empty: %+v", sc)
	}
	value(s, "AMMO", 30)
	if sc := s.Screen(apex()); sc.Lines[0].Text != "Ammo 30" {
		t.Errorf("ammo: %+v", sc.Lines)
	}
	// An Engine app below games: its later event does not cover the game.
	s.SetPriority("CLOCK", -1)
	s.Bind("CLOCK", "TIME", EventInfo{Max: 100, ValueOptional: true}, []json.RawMessage{json.RawMessage(`{"device-type":"screened","mode":"screen","datas":[{"has-text":true,"context-frame-key":"time"}]}`)})
	s.Event("CLOCK", "TIME", json.RawMessage(`{"frame":{"time":"12:00"}}`))
	if sc := s.Screen(apex()); sc.Game != "GAME" {
		t.Errorf("the clock covered the game: %+v", sc)
	}
	s.Stop("GAME")
	if sc := s.Screen(apex()); sc == nil || sc.Game != "CLOCK" || sc.Lines[0].Text != "12:00" {
		t.Errorf("after the game stopped: %+v", sc)
	}
}

func TestScreenImage(t *testing.T) {
	s, _ := newState()
	bind(t, s, "PIC", `{"device-type":"screened-128x40","mode":"screen","datas":[{"has-text":false,"image-data":[]}]}`)
	img := make([]int, 640)
	img[0] = 0x80
	b, _ := json.Marshal(map[string]any{"value": 1, "frame": map[string]any{"image-data-128x40": img}})
	s.Event("GAME", "PIC", b)
	sc := s.Screen(apex())
	if sc == nil || len(sc.Image) != 640 || sc.Image[0] != 0x80 {
		t.Fatalf("image: %+v", sc)
	}
}
