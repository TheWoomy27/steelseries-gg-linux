package engine

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"ggl/internal/devicert"

	_ "modernc.org/sqlite"
)

// bindingsEngine is an Engine with just the tables bindings use, as the
// Windows Engine's migrations make them.
func bindingsEngine(t *testing.T) *Engine {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`CREATE TABLE macros (id UUID PRIMARY KEY UNIQUE, name TEXT, events TEXT, user_id integer default (0) not null, recording_options TEXT,
			cloud_last_updated_at string DEFAULT "", is_deleted integer default (0) not null, is_new INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE "bindings" (id INTEGER PRIMARY KEY AUTOINCREMENT UNIQUE, type TEXT NOT NULL default '', config_id UUID NOT NULL,
			macro_id UUID default(0), trigger_data TEXT NOT NULL default '{}', sse_command_id UUID default(0),
			transparent_macro_data TEXT NOT NULL default '', playback_options_data TEXT NOT NULL DEFAULT '{ "type": 0, "num_repeat": 1, "repeat_delay": 15}',
			is_passthrough INTEGER NOT NULL DEFAULT 0, os_macro_type TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO macros (id, name, events) VALUES ('m1', 'Hello', '[{"type":2,"page":1,"code":11,"extraData":1,"timestamp":0},{"type":4,"page":0,"code":0,"extraData":35,"timestamp":0},{"type":2,"page":1,"code":11,"extraData":0,"timestamp":0}]')`,
		`INSERT INTO macros (id, name, events, is_deleted) VALUES ('m2', 'Gone', '[]', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return &Engine{db: db}
}

func TestBindingsRoundTrip(t *testing.T) {
	e := bindingsEngine(t)
	// As GG's configuration toJSON sends them.
	sent := `[
		{"trigger": "{\"hidCode\":4,\"page\":1,\"type\":2,\"direction\":1}", "type": "macro", "macro_id": "m1",
		 "playback_options_data": "{\"type\":1,\"num_repeat\":\"3\",\"repeat_delay\":100}", "is_passthrough": false},
		{"trigger": "{\"hidCode\":5,\"page\":1,\"type\":2,\"direction\":1}", "type": "os_macro", "os_macro_type": "copy",
		 "playback_options_data": "{\"type\":0,\"num_repeat\":1,\"repeat_delay\":15}", "is_passthrough": false},
		{"trigger": "{\"hidCode\":6,\"page\":1,\"type\":2,\"direction\":1}", "type": "sse_command", "sse_command_id": "abc",
		 "playback_options_data": "{\"type\":0,\"num_repeat\":1,\"repeat_delay\":15}", "is_passthrough": true},
		{"trigger": "{\"hidCode\":7,\"page\":1,\"type\":2,\"direction\":1}", "type": "macro", "macro_id": "m2",
		 "playback_options_data": "{\"type\":0,\"num_repeat\":1,\"repeat_delay\":15}"}
	]`
	if err := e.setBindings("c1", json.RawMessage(sent)); err != nil {
		t.Fatal(err)
	}
	got := e.bindingsOf("c1")
	if len(got) != 4 || got[0].MacroID != "m1" || got[1].OSMacroType != "copy" || got[1].MacroID != "" || got[2].SseCommandID != "abc" || !got[2].IsPassthrough {
		t.Fatalf("stored %+v", got)
	}
	// What GG reads back is what it sent.
	var back []map[string]any
	b, _ := json.Marshal(got)
	json.Unmarshal(b, &back)
	if back[0]["trigger"] != `{"hidCode":4,"page":1,"type":2,"direction":1}` || back[0]["macro_id"] != "m1" || back[1]["macro_id"] != "" {
		t.Errorf("read back %v", back[:2])
	}

	mbs := e.MacroBindings(0x1612, "c1")
	if len(mbs) != 2 {
		t.Fatalf("macro bindings %+v (the Engine command and the deleted macro are not played onboard)", mbs)
	}
	if mbs[0].Trigger != (devicert.Trigger{HIDCode: 4, Page: 1, Type: 2, Direction: 1}) || mbs[0].Playback != (devicert.Playback{Type: 1, NumRepeats: 3, RepeatDelay: 100}) || len(mbs[0].Events) != 3 || mbs[0].Events[1].ExtraData != 35 {
		t.Errorf("macro binding %+v", mbs[0])
	}
	if len(mbs[1].Events) != 5 || mbs[1].Events[0].Code != hidLeftCtrl || mbs[1].Events[1].Code != 0x06 || mbs[1].Events[2].Type != devicert.EventDelay {
		t.Errorf("copy %+v", mbs[1].Events)
	}

	// Saving replaces them.
	if err := e.setBindings("c1", json.RawMessage(`[]`)); err != nil || len(e.bindingsOf("c1")) != 0 {
		t.Errorf("after clearing: %v %+v", err, e.bindingsOf("c1"))
	}
}

func TestTransparentEvents(t *testing.T) {
	key := devicert.Trigger{HIDCode: 4, Page: 1, Type: 2, Direction: 1}
	once := devicert.Playback{}
	if _, ok := transparentEvents(key, transparentMacro{HIDCode: 4, Page: 1, Type: 2}, once); ok {
		t.Error("a key bound to itself made a macro")
	}
	if ev, ok := transparentEvents(key, transparentMacro{}, once); !ok || len(ev) != 0 {
		t.Errorf("disabled: %v %v", ev, ok)
	}
	ev, _ := transparentEvents(key, transparentMacro{HIDCode: 0x32, Type: devicert.EventMouseWheel}, once)
	if want := []devicert.MacroEvent{{Type: 1, ExtraData: -1}, {Type: 1}}; !reflect.DeepEqual(ev, want) {
		t.Errorf("scroll down: %+v", ev)
	}
	ev, _ = transparentEvents(key, transparentMacro{HIDCode: 5, Page: 1, Type: devicert.EventKeyHold}, devicert.Playback{Type: 2})
	if want := []devicert.MacroEvent{{Type: 2, Page: 1, Code: 5, ExtraData: 1}, {Type: 4, ExtraData: 1}, {Type: 2, Page: 1, Code: 5}}; !reflect.DeepEqual(ev, want) {
		t.Errorf("repeated B: %+v", ev)
	}
	ev, _ = transparentEvents(key, transparentMacro{HIDCode: 5, Page: 1, Type: devicert.EventKeyHold}, once)
	if want := []devicert.MacroEvent{{Type: 8, Page: 1, Code: 5}}; !reflect.DeepEqual(ev, want) {
		t.Errorf("B held: %+v", ev)
	}
}
