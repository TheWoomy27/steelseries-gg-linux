package prism

import (
	"encoding/json"
	"testing"

	"ggl/internal/devicert"
	"ggl/internal/engine"
	"ggl/internal/gamesense"
)

func TestGameSenseOverPrism(t *testing.T) {
	kbInfo := &devicert.GameSenseInfo{Types: []string{"device", "keyboard", "rgb-per-key-zones"},
		Zones: map[string][]int{"all": {4, 5, 0x65}, "a": {4}, "win-menu": {0x65}}, Remap: map[int]int{0x65: 0xF0}}
	kb := &devicert.PrismData{HasBitmap: true, ZoneWrites: []devicert.PrismWrite{{Data: make([]byte, 16)}},
		Zones: []devicert.PrismZone{
			{X: 100, BitmapX: 1, BitmapY: 3, HidCode: 4, Red: 1},
			{X: 200, BitmapX: 6, BitmapY: 4, HidCode: 5, Red: 4},
			{X: 300, BitmapX: 21, BitmapY: 5, HidCode: 0xF0, Red: 7},
		}}
	// The Aerox's frame: zone numbers 2, 1, 0 before the colours.
	mouseInfo := &devicert.GameSenseInfo{Types: []string{"device", "rgb-3-zone"}, Zones: map[string][]int{"one": {0}, "two": {1}, "three": {2}, "all": {0, 1, 2}}}
	mouse := &devicert.PrismData{ZoneWrites: []devicert.PrismWrite{{Data: []byte{0, 0x61, 3, 2, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0}}},
		Zones: []devicert.PrismZone{{Y: 80, Red: 4}, {Y: 40, Red: 8}, {Y: 15, Red: 12}}}
	kbDev := newDevice(engine.PrismDevice{ID: 1, Data: kb, GameSense: kbInfo}, nil, false)
	mouseDev := newDevice(engine.PrismDevice{ID: 2, Type: 1, Data: mouse, GameSense: mouseInfo}, nil, false)
	plain := newDevice(engine.PrismDevice{ID: 3, Data: mouse}, nil, false)
	if plain.gs != nil {
		t.Error("a device without GameSense info got GameSense codes")
	}
	if got := mouseDev.gs.codes; got[0] != 2 || got[1] != 1 || got[2] != 0 {
		t.Errorf("mouse zone numbers %v", got)
	}
	if g := kbDev.gs.dev.Grid; g[3*22+1] != 4 || g[5*22+21] != 0xF0 {
		t.Error("keyboard grid")
	}

	gs := gamesense.New()
	bind := func(event, handler string) {
		gs.Bind("TEST", event, gamesense.EventInfo{Max: 100}, []json.RawMessage{json.RawMessage(handler)})
	}
	bind("A", `{"device-type":"rgb-per-key-zones","zone":"a","color":{"red":255,"green":0,"blue":0}}`)
	bind("MENU", `{"device-type":"keyboard","zone":"win-menu","color":{"red":0,"green":0,"blue":255}}`)
	bind("ONE", `{"device-type":"rgb-3-zone","zone":"one","color":{"red":0,"green":255,"blue":0}}`)
	base := Color{9, 9, 9}
	colors := map[string]Color{}
	devs := []*device{kbDev, mouseDev}
	reset := func() {
		for _, d := range devs {
			for _, z := range d.Zones {
				colors[z.UniqueID] = base
			}
		}
	}
	reset()
	drawGameSense(gs, colors, devs)
	if colors[kbDev.Zones[0].UniqueID] != base {
		t.Error("lights before any event")
	}
	for _, ev := range []string{"A", "MENU", "ONE"} {
		gs.Event("TEST", ev, json.RawMessage(`{"value":1}`))
	}
	drawGameSense(gs, colors, devs)
	want := []struct {
		z    zone
		want Color
	}{
		{kbDev.Zones[0], Color{255, 0, 0}},
		{kbDev.Zones[1], base},
		{kbDev.Zones[2], Color{0, 0, 255}}, // win-menu is written as the SteelSeries key
		{mouseDev.Zones[2], Color{0, 255, 0}},
		{mouseDev.Zones[0], base},
	}
	for i, w := range want {
		if got := colors[w.z.UniqueID]; got != w.want {
			t.Errorf("%d: %v, want %v", i, got, w.want)
		}
	}
	gs.Stop("TEST")
	reset()
	drawGameSense(gs, colors, devs)
	if colors[kbDev.Zones[0].UniqueID] != base {
		t.Error("lights after stop_game")
	}
}
