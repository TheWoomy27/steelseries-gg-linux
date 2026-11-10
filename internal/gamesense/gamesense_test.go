package gamesense

import (
	"encoding/json"
	"testing"
	"time"
)

// keyboard is a per-key keyboard as the Apex 7's specification describes
// it (part of it).
func keyboard() *Device {
	grid := make([]int, 132)
	for i := range grid {
		grid[i] = -1
	}
	grid[0] = 0x29 // escape at (0, 0)
	for i := 0; i < 10; i++ {
		grid[22+1+i] = 0x1E + i // the number row from (1, 1)
	}
	all := []int{0x29, 0x3A, 0x3B, 0x3C, 0x3D}
	for i := 0; i < 10; i++ {
		all = append(all, 0x1E+i)
	}
	return &Device{ProductID: 0x1612, Types: []string{"device", "keyboard", "rgb-per-key-zones", "rgb"}, Grid: grid,
		Zones: map[string][]int{"all": all, "escape": {0x29}, "function-keys": {0x3A, 0x3B, 0x3C, 0x3D},
			"number-keys": {0x1E, 0x1F, 0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27}}}
}

func mouse() *Device {
	return &Device{ProductID: 0x1852, Types: []string{"device", "rgb-3-zone", "rgb"},
		Zones: map[string][]int{"one": {0}, "two": {1}, "three": {2}, "all": {0, 1, 2}}}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newState() (*State, *clock) {
	s := New()
	c := &clock{time.Unix(1000, 0)}
	s.Now = c.now
	return s, c
}

func bind(t *testing.T, s *State, event string, handlers ...string) {
	t.Helper()
	var raws []json.RawMessage
	for _, h := range handlers {
		raws = append(raws, json.RawMessage(h))
	}
	if n := s.Bind("GAME", event, EventInfo{Max: 100}, raws); n != len(handlers) {
		t.Fatalf("bound %d of %d handlers", n, len(handlers))
	}
}

func value(s *State, event string, v float64) {
	b, _ := json.Marshal(map[string]any{"value": v})
	s.Event("GAME", event, b)
}

func TestColors(t *testing.T) {
	for _, c := range []struct {
		def  string
		v    float64
		want Color
	}{
		{`{"red":255,"green":0,"blue":255}`, 1, Color{255, 0, 255}},
		{`{"red":255,"green":0,"blue":255}`, 0, Color{}},
		{`{"gradient":{"zero":{"red":255,"green":0,"blue":0},"hundred":{"red":0,"green":255,"blue":0}}}`, 25, Color{191, 63, 0}},
		{`{"gradient":{"zero":{"red":255,"green":0,"blue":0},"hundred":{"red":0,"green":255,"blue":0}}}`, 0, Color{255, 0, 0}},
		// Ranges: inclusive, the outer ends open, a static colour shown
		// even for 0 inside them.
		{`[{"low":0,"high":10,"color":{"red":255,"green":0,"blue":0}},{"low":11,"high":100,"color":{"gradient":{"zero":{"red":0,"green":0,"blue":0},"hundred":{"red":0,"green":0,"blue":200}}}}]`, 0, Color{255, 0, 0}},
		{`[{"low":0,"high":10,"color":{"red":255,"green":0,"blue":0}},{"low":11,"high":100,"color":{"gradient":{"zero":{"red":0,"green":0,"blue":0},"hundred":{"red":0,"green":0,"blue":200}}}}]`, 50, Color{0, 0, 100}},
		{`[{"low":0,"high":10,"color":{"red":255,"green":0,"blue":0}},{"low":11,"high":100,"color":{"red":0,"green":9,"blue":0}}]`, 500, Color{0, 9, 0}},
		{`[{"low":0,"high":10,"color":{"red":255,"green":0,"blue":0}},{"low":11,"high":100,"color":{"red":0,"green":9,"blue":0}}]`, -5, Color{255, 0, 0}},
		{`[{"low":0,"high":10,"color":{"red":255,"green":0,"blue":0}},{"low":20,"high":100,"color":{"red":0,"green":9,"blue":0}}]`, 15, Color{}},
	} {
		if got := parseColor(json.RawMessage(c.def), true)(c.v, false); got != c.want {
			t.Errorf("%s at %v: %v, want %v", c.def, c.v, got, c.want)
		}
	}
}

func TestZonesAndModes(t *testing.T) {
	s, _ := newState()
	bind(t, s, "HEALTH", `{"device-type":"keyboard","zone":"function-keys","mode":"percent","color":{"red":0,"green":200,"blue":0}}`,
		`{"device-type":"rgb-3-zone","zone":"two","mode":"percent","color":{"red":0,"green":200,"blue":0}}`)
	bind(t, s, "AMMO", `{"device-type":"rgb-per-key-zones","zone":"number-keys","mode":"count","color":{"red":0,"green":0,"blue":255}}`)
	bind(t, s, "WASD", `{"device-type":"rgb-per-key-zones","custom-zone-keys":[41,26,4,22,7],"color":{"red":1,"green":2,"blue":3}}`)
	kb, ms := keyboard(), mouse()
	if s.Lights(kb) != nil || s.Active() {
		t.Fatal("lights before any event")
	}

	// 3 of 4 keys at 70%: two full, the third at 80% of its 25.
	value(s, "HEALTH", 70)
	got := s.Lights(kb)
	want := map[int]Color{0x3A: {0, 200, 0}, 0x3B: {0, 200, 0}, 0x3C: {0, 160, 0}, 0x3D: {}}
	for k, c := range want {
		if got[k] != c {
			t.Errorf("health key %#x: %v, want %v", k, got[k], c)
		}
	}
	if len(got) != 4 {
		t.Errorf("health lit %d keys: %v", len(got), got)
	}
	// A zoned device shows the colour.
	if got := s.Lights(ms); len(got) != 1 || got[1] != (Color{0, 200, 0}) {
		t.Errorf("mouse: %v", got)
	}

	value(s, "AMMO", 3)
	got = s.Lights(kb)
	for i := 0; i < 10; i++ {
		want := Color{}
		if i < 3 {
			want = Color{0, 0, 255}
		}
		if got[0x1E+i] != want {
			t.Errorf("ammo key %d: %v, want %v", i, got[0x1E+i], want)
		}
	}

	// Custom keys outside the device are dropped (escape is in it).
	value(s, "WASD", 1)
	got = s.Lights(kb)
	if got[0x29] != (Color{1, 2, 3}) || len(got) != 4+10+1 {
		t.Errorf("custom keys: %v", got)
	}
}

func TestLatestWinsAndTimeout(t *testing.T) {
	s, c := newState()
	var changes []string
	s.OnActive = func(g string, on bool) { changes = append(changes, g+map[bool]string{true: " on", false: " off"}[on]) }
	bind(t, s, "A", `{"device-type":"rgb-3-zone","zone":"one","color":{"red":255,"green":0,"blue":0}}`)
	bind(t, s, "B", `{"device-type":"rgb-3-zone","zone":"all","color":{"red":0,"green":0,"blue":255}}`)
	ms := mouse()
	value(s, "A", 1)
	value(s, "B", 1)
	if got := s.Lights(ms); got[0] != (Color{0, 0, 255}) {
		t.Errorf("B came last: %v", got)
	}
	c.t = c.t.Add(time.Second)
	value(s, "A", 2)
	if got := s.Lights(ms); got[0] != (Color{255, 0, 0}) || got[1] != (Color{0, 0, 255}) {
		t.Errorf("A came last: %v", got)
	}
	// The same value again changes nothing.
	c.t = c.t.Add(time.Second)
	value(s, "B", 1)
	if got := s.Lights(ms); got[0] != (Color{255, 0, 0}) {
		t.Errorf("an unchanged value took over: %v", got)
	}
	// 15 s without events: the game stops and its lights go.
	c.t = c.t.Add(14 * time.Second)
	s.Heartbeat("GAME")
	c.t = c.t.Add(14 * time.Second)
	if !s.Active() {
		t.Error("the heartbeat did not keep the game going")
	}
	c.t = c.t.Add(2 * time.Second)
	if s.Active() || s.Lights(ms) != nil {
		t.Error("the game did not time out")
	}
	// Its values are gone: the next event starts afresh.
	value(s, "B", 1)
	if got := s.Lights(ms); got[0] != (Color{0, 0, 255}) {
		t.Errorf("after the timeout: %v", got)
	}
	s.Stop("GAME")
	if s.Active() {
		t.Error("stop_game did not stop it")
	}
	if want := []string{"GAME on", "GAME off", "GAME on", "GAME off"}; len(changes) != len(want) || changes[1] != want[1] || changes[3] != want[3] {
		t.Errorf("notifications %v, want %v", changes, want)
	}
}

func TestFlashing(t *testing.T) {
	s, c := newState()
	bind(t, s, "HIT", `{"device-type":"keyboard","zone":"escape","color":{"red":255,"green":0,"blue":0},"rate":{"frequency":2,"repeat_limit":2}}`)
	kb := keyboard()
	start := c.t
	value(s, "HIT", 1)
	red := Color{255, 0, 0}
	for _, step := range []struct {
		ms   int
		want Color
	}{{0, red}, {249, red}, {250, Color{}}, {500, red}, {750, Color{}}, {1000, red}, {1250, red}, {5000, red}} {
		c.t = start.Add(time.Duration(step.ms) * time.Millisecond)
		if got := s.Lights(kb)[0x29]; got != step.want {
			t.Errorf("at %d ms: %v, want %v", step.ms, got, step.want)
		}
	}
	// Frequency ranges: none outside them.
	h, _ := ParseHandler(json.RawMessage(`{"device-type":"keyboard","rate":{"frequency":[{"low":0,"high":10,"frequency":10},{"low":11,"high":20,"frequency":5}]}}`))
	if h.freq(5) != 50 || h.freq(15) != 100 || h.freq(30) != 0 {
		t.Errorf("frequency ranges: %d %d %d", h.freq(5), h.freq(15), h.freq(30))
	}
}

func TestContextAndBitmap(t *testing.T) {
	s, _ := newState()
	bind(t, s, "CTX", `{"device-type":"rgb-3-zone","zone":"three","mode":"context-color","context-frame-key":"zone-three-color"}`)
	bind(t, s, "PIC", `{"device-type":"rgb-per-key-zones","mode":"partial-bitmap","excluded-events":["KEYS"]}`)
	bind(t, s, "KEYS", `{"device-type":"keyboard","zone":"number-keys","color":{"red":9,"green":9,"blue":9}}`)
	s.Event("GAME", "CTX", json.RawMessage(`{"frame":{"zone-three-color":{"red":0,"green":255,"blue":0}}}`))
	if got := s.Lights(mouse()); got[2] != (Color{0, 255, 0}) {
		t.Errorf("context colour: %v", got)
	}
	// A string holding the data works as well.
	s.Event("GAME", "CTX", json.RawMessage(`"{\"frame\":{\"zone-three-color\":{\"red\":1,\"green\":2,\"blue\":3}}}"`))
	if got := s.Lights(mouse()); got[2] != (Color{1, 2, 3}) {
		t.Errorf("context colour from a string: %v", got)
	}

	pix := make([][3]int, 132)
	for i := range pix {
		pix[i] = [3]int{i, 0, 0}
	}
	b, _ := json.Marshal(map[string]any{"frame": map[string]any{"bitmap": pix}})
	s.Event("GAME", "PIC", b)
	got := s.Lights(keyboard())
	if got[0x29] != (Color{0, 0, 0}) || len(got) != 1 {
		t.Errorf("partial bitmap without the number keys: %v", got)
	}
	b, _ = json.Marshal(map[string]any{"frame": map[string]any{"bitmap": pix, "excluded-events": []string{}}})
	s.Event("GAME", "PIC", b)
	got = s.Lights(keyboard())
	if got[0x1E] != (Color{23, 0, 0}) || got[0x27] != (Color{32, 0, 0}) || len(got) != 11 {
		t.Errorf("full bitmap: %v", got)
	}
}

func TestDisabledGame(t *testing.T) {
	s, _ := newState()
	bind(t, s, "A", `{"device-type":"rgb-3-zone","zone":"one","color":{"red":255,"green":0,"blue":0}}`)
	s.SetEnabled("game", false)
	value(s, "A", 1)
	if s.Active() || s.Lights(mouse()) != nil {
		t.Error("a disabled game lights devices")
	}
	s.SetEnabled("GAME", true)
	value(s, "A", 1)
	if s.Lights(mouse()) == nil {
		t.Error("re-enabled game does not light")
	}
	// Screen and tactile handlers are not lighting.
	for _, h := range []string{`{"device-type":"screened","zone":"one","mode":"screen","datas":[]}`, `{"device-type":"tactile","zone":"one","mode":"vibrate","pattern":[]}`} {
		if _, ok := ParseHandler(json.RawMessage(h)); ok {
			t.Errorf("%s parsed as lighting", h)
		}
	}
}

func TestUserHandler(t *testing.T) {
	s, _ := newState()
	bind(t, s, "HEALTH", `{"device-type":"rgb-3-zone","zone":"one","color":{"red":255,"green":0,"blue":0}}`)
	ms := mouse()
	other := mouse()
	other.ProductID = 0x1854
	// The user's handler for one device replaces the game's there only.
	s.SetUserHandler("GAME", "HEALTH", ms.ProductID, json.RawMessage(`{"device-id":6226,"zone":"two","mode":"color","color":{"red":0,"green":0,"blue":255}}`))
	value(s, "HEALTH", 1)
	if got := s.Lights(ms); len(got) != 1 || got[1] != (Color{0, 0, 255}) {
		t.Errorf("user handler: %v", got)
	}
	if got := s.Lights(other); got[0] != (Color{255, 0, 0}) {
		t.Errorf("another device: %v", got)
	}
	// Turned off for the device.
	s.SetUserHandler("GAME", "HEALTH", ms.ProductID, json.RawMessage(`{"device-id":6226,"mode":"disabled"}`))
	value(s, "HEALTH", 2)
	if got := s.Lights(ms); len(got) != 0 {
		t.Errorf("disabled on the device: %v", got)
	}
	// "{}" gives it back to the game.
	s.SetUserHandler("GAME", "HEALTH", ms.ProductID, json.RawMessage(`{}`))
	value(s, "HEALTH", 3)
	if got := s.Lights(ms); got[0] != (Color{255, 0, 0}) {
		t.Errorf("back to the game's: %v", got)
	}
}
