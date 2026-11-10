package engine

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"ggl/internal/devicert"
	"ggl/internal/hidkeys"
)

func decode(t *testing.T, s string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// A mouse button or key bound to something ggd runs is remapped to send
// its spare usage; the settings it came from are left alone.
func TestRemapOutputs(t *testing.T) {
	mouse := decode(t, `{"button_mappings": {"buttons": [
		{"function": 1, "key_codes": [0, 0, 0, 0]}, {"function": 2, "key_codes": [0, 0, 0, 0]},
		{"function": 4, "key_codes": [0, 0, 0, 0]}, {"function": 49, "key_codes": [0, 0, 0, 0]}]}, "polling_rate": {"level": 1}}`)
	binds := []*softBinding{
		{trig: devicert.Trigger{HIDCode: 4, Page: 1, Type: 0, Direction: 1}, spare: 0xa5},
		{trig: devicert.Trigger{HIDCode: 0x31, Page: 1, Type: 1}, spare: 0xa6},
		{trig: devicert.Trigger{HIDCode: 2, Page: 1, Type: 0}, pass: true}, // passthrough: no spare
	}
	out, changed := remapOutputs(mouse, binds)
	if !changed {
		t.Fatal("nothing remapped")
	}
	buttons := out.(map[string]any)["button_mappings"].(map[string]any)["buttons"].([]any)
	want := []string{
		`{"function":1,"key_codes":[0,0,0,0]}`, `{"function":2,"key_codes":[0,0,0,0]}`,
		`{"function":81,"key_codes":[165,0,0,0]}`, `{"function":81,"key_codes":[166,0,0,0]}`,
	}
	for i, w := range want {
		if b, _ := json.Marshal(buttons[i]); string(b) != w {
			t.Errorf("button %d: %s, want %s", i, b, w)
		}
	}
	if b, _ := json.Marshal(mouse["button_mappings"].(map[string]any)["buttons"].([]any)[2]); string(b) != `{"function":4,"key_codes":[0,0,0,0]}` {
		t.Errorf("the original settings changed: %s", b)
	}

	keyboard := decode(t, `{"mappings": {"button_mappings": {"buttons": [
		{"hid_code": 4, "function": 81, "key_codes": [4, 0, 0, 0]}, {"hid_code": 5, "function": 81, "key_codes": [5, 225, 0, 0]}]}}}`)
	out, _ = remapOutputs(keyboard, []*softBinding{
		{trig: devicert.Trigger{HIDCode: 4, Page: 1, Type: 2}, spare: 0xa5},
		{trig: devicert.Trigger{HIDCode: 5, Page: 1, Type: 2}, spare: 0xa6}, // a combo with Shift is not plain B
	})
	keys := out.(map[string]any)["mappings"].(map[string]any)["button_mappings"].(map[string]any)["buttons"].([]any)
	if b, _ := json.Marshal(keys); string(b) != `[{"function":81,"hid_code":4,"key_codes":[165,0,0,0]},{"function":81,"hid_code":5,"key_codes":[5,225,0,0]}]` {
		t.Errorf("keyboard: %s", b)
	}
}

type fakePlayer struct {
	mu  sync.Mutex
	log []string
}

func (p *fakePlayer) Key(code uint16, down bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.log = append(p.log, fmt.Sprintf("%d %v", code, down))
	return nil
}
func (p *fakePlayer) Wheel(n int32, h bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.log = append(p.log, fmt.Sprintf("wheel %d %v", n, h))
	return nil
}
func (p *fakePlayer) Close() {}

func (p *fakePlayer) take() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.log
	p.log = nil
	return l
}

func waitFor(t *testing.T, p *fakePlayer, n int) []string {
	t.Helper()
	var got []string
	for i := 0; i < 100 && len(got) < n; i++ {
		got = append(got, p.take()...)
		time.Sleep(5 * time.Millisecond)
	}
	return got
}

func TestPlayBindings(t *testing.T) {
	p := &fakePlayer{}
	e := &Engine{}
	e.soft.player = p
	key := func(usage uint16, down int64) devicert.MacroEvent {
		return devicert.MacroEvent{Type: devicert.EventKey, Page: 1, Code: usage, ExtraData: down}
	}
	a := fmt.Sprint(hidkeys.Keyboard[4]) // KEY_A
	once := &softBinding{trig: devicert.Trigger{Type: 0, HIDCode: 4, Direction: 1},
		events: []devicert.MacroEvent{key(4, 1), {Type: devicert.EventDelay, ExtraData: 5}, key(4, 0), {Type: devicert.EventMouseWheel, ExtraData: -1}}}
	e.trigger(once, true)
	if got := waitFor(t, p, 3); !reflect.DeepEqual(got, []string{a + " true", a + " false", "wheel -1 false"}) {
		t.Errorf("play once: %v", got)
	}
	e.trigger(once, false)
	if got := waitFor(t, p, 1); len(got) != 0 {
		t.Errorf("released: %v", got)
	}

	// Played on release, and a key left down is let go at the end.
	onUp := &softBinding{trig: devicert.Trigger{Type: 0, HIDCode: 4, Direction: 0}, events: []devicert.MacroEvent{key(4, 1)}}
	e.trigger(onUp, true)
	if got := waitFor(t, p, 1); len(got) != 0 {
		t.Errorf("played on press: %v", got)
	}
	e.trigger(onUp, false)
	if got := waitFor(t, p, 2); !reflect.DeepEqual(got, []string{a + " true", a + " false"}) {
		t.Errorf("on release: %v", got)
	}

	// Repeat while pressed: runs until the button is let go.
	repeat := &softBinding{trig: devicert.Trigger{Type: 0, HIDCode: 4, Direction: 1}, play: devicert.Playback{Type: 2, RepeatDelay: 5},
		events: []devicert.MacroEvent{key(4, 1), key(4, 0)}}
	e.trigger(repeat, true)
	got := waitFor(t, p, 6)
	e.trigger(repeat, false)
	time.Sleep(30 * time.Millisecond)
	p.take()
	time.Sleep(30 * time.Millisecond)
	if len(got) < 6 || len(p.take()) != 0 {
		t.Errorf("repeat while pressed: %v, then still playing", got)
	}

	// Toggle repeat: the second press stops it.
	repeat.play.Type = 3
	e.trigger(repeat, true)
	e.trigger(repeat, false)
	if got := waitFor(t, p, 4); len(got) < 4 {
		t.Errorf("toggle repeat didn't start: %v", got)
	}
	e.trigger(repeat, true)
	time.Sleep(30 * time.Millisecond)
	p.take()
	time.Sleep(30 * time.Millisecond)
	if got := p.take(); len(got) != 0 {
		t.Errorf("toggle repeat didn't stop: %v", got)
	}

	// A key held for as long as the trigger is (a transparent macro).
	hold := &softBinding{trig: devicert.Trigger{Type: 0, HIDCode: 4, Direction: 1}, events: []devicert.MacroEvent{{Type: devicert.EventKeyHold, Page: 1, Code: 5}}}
	b := fmt.Sprint(hidkeys.Keyboard[5])
	e.trigger(hold, true)
	if got := waitFor(t, p, 1); !reflect.DeepEqual(got, []string{b + " true"}) {
		t.Errorf("hold down: %v", got)
	}
	e.trigger(hold, false)
	if got := waitFor(t, p, 1); !reflect.DeepEqual(got, []string{b + " false"}) {
		t.Errorf("hold up: %v", got)
	}
}

// The bindings ggd runs for a mouse without macro memory: its macro and
// its Engine command, with spare usages in order; a keyboard with macro
// memory plays its macros itself.
func TestSoftBindingsFor(t *testing.T) {
	e := bindingsEngine(t)
	if _, err := e.db.Exec(`CREATE TABLE sse_commands (id UUID UNIQUE, local_id INTEGER PRIMARY KEY AUTOINCREMENT UNIQUE, user_id INTEGER NOT NULL,
		command_type TEXT NOT NULL default '', display_name TEXT NOT NULL DEFAULT '', path TEXT NOT NULL DEFAULT '', image_path TEXT NOT NULL DEFAULT '',
		params TEXT NOT NULL DEFAULT '', linked_application_id INT NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	e.db.Exec(`INSERT INTO sse_commands (id, user_id, command_type, display_name, path) VALUES ('s1', 1, 'launch_application', 'Terminal', '/usr/bin/foot')`)
	e.setBindings("c1", json.RawMessage(`[
		{"trigger": "{\"hidCode\":4,\"page\":1,\"type\":0,\"direction\":1}", "type": "macro", "macro_id": "m1"},
		{"trigger": "{\"hidCode\":5,\"page\":1,\"type\":0,\"direction\":1}", "type": "sse_command", "sse_command_id": "s1"},
		{"trigger": "{\"hidCode\":3,\"page\":1,\"type\":0,\"direction\":1}", "type": "sse_command", "sse_command_id": "s1", "is_passthrough": true}
	]`))
	mouse := &liveDevice{cat: &catalogDevice{Name: "aerox_5_wireless"}}
	binds := e.softBindingsFor(mouse, "c1")
	if len(binds) != 3 || len(binds[0].events) != 3 || binds[1].cmd == nil || binds[1].cmd.Path != "/usr/bin/foot" {
		t.Fatalf("mouse bindings %+v", binds)
	}
	if binds[0].spare != hidkeys.Spare[0] || binds[1].spare != hidkeys.Spare[1] || binds[2].spare != 0 {
		t.Errorf("spares %#x %#x %#x", binds[0].spare, binds[1].spare, binds[2].spare)
	}
	keyboard := &liveDevice{cat: &catalogDevice{Name: "apex_7", HasOnboardMacros: true}}
	if binds := e.softBindingsFor(keyboard, "c1"); len(binds) != 2 || binds[0].cmd == nil {
		t.Errorf("keyboard bindings %+v", binds)
	}
}
