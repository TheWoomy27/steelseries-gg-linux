package devicert

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/steelseries/golisp"
)

func TestEncodeMacro(t *testing.T) {
	b := MacroBinding{
		Trigger:  Trigger{HIDCode: 4, Page: 1, Type: 2, Direction: 1},
		Playback: Playback{Type: 1, NumRepeats: 3, RepeatDelay: 300},
		Events: []MacroEvent{
			{Type: EventMouseButton, Code: 2, ExtraData: 1},
			{Type: EventKey, Page: 7, Code: 5, ExtraData: 1},
			{Type: EventDelay, ExtraData: 70000},
			{Type: EventKey, Page: 12, Code: 0xe9, ExtraData: 0},
			{Type: EventMouseWheel, ExtraData: -1},
			{Type: EventMouseWheel, ExtraData: 0},
		},
	}
	h, items, err := encodeMacro(&b)
	if err != nil {
		t.Fatal(err)
	}
	if want := [8]byte{0x77, 2, 6, 0, 1, 3, 0x2c, 0x01}; h != want {
		t.Errorf("header % x, want % x", h, want)
	}
	want := [][4]byte{{0, 2, 0, 1}, {2, 5, 1, 1}, {4, 0, 0xff, 0xff}, {4, 0, 0x71, 0x11}, {2, 0xe9, 12, 0}, {1, 0, 0, 0xff}}
	if len(items) != len(want) {
		t.Fatalf("items %v, want %v", items, want)
	}
	for i := range want {
		if items[i] != want[i] {
			t.Errorf("item %d: % x, want % x", i, items[i], want[i])
		}
	}
	b.Events = []MacroEvent{{Type: 13, Code: 1}}
	if _, _, err := encodeMacro(&b); err == nil {
		t.Error("an extra mouse button went onboard")
	}
}

func TestConvertOnboardMacros(t *testing.T) {
	maps := []KeyMapping{{HIDCode: 4, Function: 0x51, KeyCodes: [4]uint8{4}}, {HIDCode: 5, Function: 0x51, KeyCodes: [4]uint8{5}}, {HIDCode: 6, Function: 0x51, KeyCodes: [4]uint8{6}}}
	trigs := []Trigger{{HIDCode: 4, Page: 1, Type: 2}, {HIDCode: 5, Page: 1, Type: 2}, {HIDCode: 6, Page: 1, Type: 2}}
	key := func(code uint16, down int64) MacroEvent {
		return MacroEvent{Type: EventKey, Page: 1, Code: code, ExtraData: down}
	}
	bindings := []MacroBinding{
		{Trigger: Trigger{HIDCode: 6, Page: 1, Type: 2, Direction: 1}, Events: []MacroEvent{key(7, 1), key(7, 0)}},
		{Trigger: Trigger{HIDCode: 4, Page: 1, Type: 2, Direction: 1}, Events: []MacroEvent{key(8, 1), {Type: EventDelay, ExtraData: 20}, key(8, 0)}},
		{Trigger: Trigger{HIDCode: 5, Page: 254, Type: 2}, Events: []MacroEvent{key(9, 1)}}, // the meta layer
	}
	out, data, skipped := ConvertOnboardMacros(maps, trigs, bindings, 64, 0x71)
	if len(skipped) > 0 {
		t.Fatal(skipped)
	}
	if want := (KeyMapping{HIDCode: 4, Function: 0x71}); out[0] != want {
		t.Errorf("A: %+v, want %+v", out[0], want)
	}
	if out[1] != maps[1] {
		t.Errorf("B changed: %+v", out[1])
	}
	// The second macro starts after the first's 8 + 3×4 bytes, at word 5.
	if want := (KeyMapping{HIDCode: 6, Function: 0x71, KeyCodes: [4]uint8{0, 0, 5, 0}}); out[2] != want {
		t.Errorf("C: %+v, want %+v", out[2], want)
	}
	wantData := []byte{0x77, 1, 3, 0, 1, 0, 0, 0, 2, 8, 1, 1, 4, 0, 20, 0, 2, 8, 1, 0, 0x77, 1, 2, 0, 1, 0, 0, 0, 2, 7, 1, 1, 2, 7, 1, 0}
	if !bytes.Equal(data[:len(wantData)], wantData) || len(data) != 64 {
		t.Errorf("data % x\nwant % x", data, wantData)
	}
	// No room: the key keeps its mapping.
	out, _, skipped = ConvertOnboardMacros(maps, trigs, bindings, 30, 0x71)
	if len(skipped) != 1 || out[2] != maps[2] || out[0].Function != 0x71 {
		t.Errorf("with 30 bytes: %+v, skipped %v", out, skipped)
	}
}

type macroHooks struct {
	nopHooks
	bindings []MacroBinding
	asked    []string
}

func (h *macroHooks) MacroBindings(devID uint32, config string) []MacroBinding {
	h.asked = append(h.asked, config)
	return h.bindings
}

// TestApexOnboardMacro writes the Apex 7's key mappings (the real spec,
// from the data gg-setup imports) with a macro on A: A's mapping points
// at the macro, which goes into the first macro chunk.
func TestApexOnboardMacro(t *testing.T) {
	data := os.Getenv("GG_LINUX_DATA")
	if data == "" {
		t.Skip("GG_LINUX_DATA not set")
	}
	hooks := &macroHooks{bindings: []MacroBinding{{
		Trigger:  Trigger{HIDCode: 4, Page: 1, Type: 2, Direction: 1},
		Playback: Playback{NumRepeats: 1, RepeatDelay: 15},
		Events:   []MacroEvent{{Type: EventKey, Page: 1, Code: 5, ExtraData: 1}, {Type: EventDelay, ExtraData: 50}, {Type: EventKey, Page: 1, Code: 5, ExtraData: 0}},
	}}}
	tr := &recordingTransport{}
	rt, err := New(filepath.Join(data, "engine", "deviceSpecifications"), filepath.Join(data, "engine"), tr, hooks)
	if err != nil {
		t.Fatal(err)
	}
	d, err := rt.Device(0x10381612)
	if err != nil {
		t.Fatal(err)
	}
	codes, err := d.Call("device-mapping-hid-codes")
	if err != nil {
		t.Fatal(err)
	}
	var buttons, meta []any
	for c := codes; golisp.NotNilP(c); c = golisp.Cdr(c) {
		code := golisp.IntegerValue(golisp.Car(c))
		buttons = append(buttons, map[string]any{"hid_code": code, "function": 0x51, "key_codes": []any{code, 0, 0, 0}})
		meta = append(meta, map[string]any{"hid_code": code, "function": 0, "key_codes": []any{0, 0, 0, 0}})
	}
	settings := map[string]any{
		"button_mappings":      map[string]any{"buttons": buttons},
		"button_meta_mappings": map[string]any{"buttons": meta},
	}
	if err := d.WriteApi("mappings", settings); err != nil {
		t.Fatal(err)
	}
	if len(hooks.asked) != 1 || hooks.asked[0] != "" {
		t.Errorf("asked for configurations %q", hooks.asked)
	}
	var keys, macro []byte
	for _, c := range tr.sent {
		switch {
		case len(c.data) > 4 && c.data[1] == 0x63 && c.data[2] == 0 && keys == nil:
			keys = c.data
		case len(c.data) > 6 && c.data[1] == 0x71 && c.data[2] == 0 && c.data[3] == 0:
			macro = c.data
		}
	}
	if keys == nil || macro == nil {
		t.Fatalf("no key mappings or first macro chunk among %d chunks", len(tr.sent))
	}
	// [0 0x63 0 count] then 6 bytes per key: A comes first.
	if got, want := keys[4:10], []byte{4, 0x71, 0, 0, 0, 0}; !bytes.Equal(got, want) {
		t.Errorf("A's mapping % x, want % x", got, want)
	}
	if got, want := keys[10:16], []byte{5, 0x51, 5, 0, 0, 0}; !bytes.Equal(got, want) {
		t.Errorf("B's mapping % x, want % x", got, want)
	}
	// [0 0x71 offset/4 u16 600/4 u16] then the macro.
	want := []byte{0x77, 1, 3, 0, 1, 1, 15, 0, 2, 5, 1, 1, 4, 0, 50, 0, 2, 5, 1, 0}
	if got := macro[6 : 6+len(want)]; !bytes.Equal(got, want) {
		t.Errorf("macro % x, want % x", got, want)
	}
}
