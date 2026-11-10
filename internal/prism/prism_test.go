package prism

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"ggl/internal/paths"

	"ggl/internal/devicert"
	"ggl/internal/engine"
	"ggl/internal/gamesense"
)

func TestGradient(t *testing.T) {
	red, green, blue := Color{255, 0, 0}, Color{0, 255, 0}, Color{0, 0, 255}
	g := newGradient([]ColorPosition{{blue, 170}, {red, 0}, {green, 85}})
	for _, c := range []struct {
		v    int
		want Color
	}{
		{0, red}, {85 << 8, green}, {170 << 8, blue}, {65535, red}, {(85 << 8) / 2, Color{128, 127, 0}},
	} {
		if got := g.at(uint16(c.v)); got != c.want {
			// Interpolation rounds down, so allow one step.
			if absDiff(got.Red, c.want.Red) > 1 || absDiff(got.Green, c.want.Green) > 1 || absDiff(got.Blue, c.want.Blue) > 1 {
				t.Errorf("at(%d) = %v, want %v", c.v, got, c.want)
			}
		}
	}
	if one := newGradient([]ColorPosition{{red, 0}}); one.at(40000) != red {
		t.Error("a single colour should fill")
	}
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func TestWave(t *testing.T) {
	// No spatial spread: every zone moves together, a full cycle per
	// duration.
	w := waveParams{Duration: 1000}
	if v, ok := waveValue(w, 250, 1234); !ok || v != 16384 {
		t.Errorf("value at a quarter = %d, %v", v, ok)
	}
	// λ = 64: the pattern repeats every 32·64 phase units.
	w = waveParams{Duration: 1000, Wavelength: 64}
	a, _ := waveValue(w, 0, 100)
	b, _ := waveValue(w, 0, 100+2048)
	if a != b {
		t.Errorf("spatial period: %d vs %d", a, b)
	}
	c, _ := waveValue(w, 0, 100+1024)
	if int(c)-int(a) != 32768 && int(a)-int(c) != 32768 {
		t.Errorf("half a period apart should differ by half: %d %d", a, c)
	}
	if phaseIndex(shapeHorizontalRow, 10, 5) != -1 || phaseIndex(shapeHorizontal, -10, 99) != 10 || phaseIndex(shapeRadial, 3, 4) != 5 {
		t.Error("phase shapes")
	}
}

type fakeEngine struct {
	devs   []engine.PrismDevice
	frames map[int][]devicert.PrismWrite
	gs     *gamesense.State
}

func (f *fakeEngine) PrismSyncDevices() []engine.PrismDevice { return f.devs }
func (f *fakeEngine) GameSense() *gamesense.State {
	if f.gs == nil {
		f.gs = gamesense.New()
	}
	return f.gs
}
func (f *fakeEngine) PrismWrite(id int, w []devicert.PrismWrite) error {
	f.frames[id] = w
	return nil
}
func (f *fakeEngine) PrismRelease(int)                        {}
func (f *fakeEngine) PrismLightingEnabled() bool              { return true }
func (f *fakeEngine) WatchPrismInput(func(engine.PrismInput)) {}
func (f *fakeEngine) OnPrismDevicesChanged(func())            {}

func testDevices() []engine.PrismDevice {
	kb := &devicert.PrismData{Supported: true, Whitelisted: true, StepMs: 33, HasBitmap: true,
		ZoneWrites: []devicert.PrismWrite{{Type: 1, Size: 16, Data: make([]byte, 16)}},
		Zones: []devicert.PrismZone{
			{X: 100, Y: 100, BitmapX: 0, BitmapY: 0, HidCode: 4, Red: 4, Green: 5, Blue: 6},
			{X: 900, Y: 100, BitmapX: 20, BitmapY: 0, HidCode: 5, Red: 8, Green: 9, Blue: 10},
		}}
	mic := &devicert.PrismData{Supported: true, Whitelisted: true, StepMs: 33, CoordinatePlane: 1,
		ZoneWrites: []devicert.PrismWrite{{Type: 0, Size: 8, Data: make([]byte, 8)}},
		Zones:      []devicert.PrismZone{{X: 0, Y: 0, Red: 3, Green: 4, Blue: 5}}}
	return []engine.PrismDevice{{ID: 159, Type: 0, Name: "apex_7", FullName: "Apex 7", Data: kb}, {ID: 280, Type: 12, Name: "alias", FullName: "Alias", Data: mic}}
}

func TestDevicesAndFrames(t *testing.T) {
	fe := &fakeEngine{devs: testDevices(), frames: map[int][]devicert.PrismWrite{}}
	d := newDevice(fe.devs[0], nil, false)
	if len(d.Zones) != 2 || d.Zones[0].AdjustedX != 31100 || d.Zones[0].AdjustedY != 30100 || d.BitmapXSize != 21 {
		t.Fatalf("keyboard: %+v", d)
	}
	if d.Zones[0].UniqueID != "44754950800277504" { // 159<<48 | 100<<32 | 100<<16
		t.Errorf("zone id %s", d.Zones[0].UniqueID)
	}
	f := d.frame(map[string]Color{d.Zones[0].UniqueID: {1, 2, 3}, d.Zones[1].UniqueID: {7, 8, 9}})
	if got := f[0].Data[4:11]; string(got) != string([]byte{1, 2, 3, 0, 7, 8, 9}) {
		t.Errorf("frame %v", f[0].Data)
	}
	mic := newDevice(fe.devs[1], nil, false)
	if mic.Zones[0].AdjustedY != 30600 {
		t.Errorf("the mic sits on the wall above the desk: %+v", mic.Zones[0])
	}
}

const colorShift = `{"base":{"globalConfig":{"metadata":{"effectType":"COLOR_SHIFT"},"graphics":[{"type":0,"zIndex":0,"uniqueId":"g1","hasForeground":true,
"foreground":{"duration":4000,"spatialWavelength":64,"phaseMatrixShape":0,"repetitions":0,"origin":{"x":0,"y":0},"isOriginFixed":false,
"gradient":{"colors":[{"position":0,"color":{"red":255,"green":0,"blue":0}},{"position":128,"color":{"red":0,"green":0,"blue":255}}]}},
"hasBackground":false,"background":{},"hasAperture":false,"aperture":{}}],"foundation":{"origin":{"x":0,"y":0},"opacity":255,"graphicId":"g1"}},
"deviceTypeConfigs":[],"perKeyConfigs":[],"deviceConfigs":[]},"idle":{"globalConfig":{"graphics":[],"foundation":{}}},"idleTimeMs":0,"reactive":{"globalConfig":{"graphics":[],"foundation":{}}}}`

func TestMutateAndRender(t *testing.T) {
	fe := &fakeEngine{devs: testDevices(), frames: map[int][]devicert.PrismWrite{}}
	mig := ""
	if _, err := os.Stat(paths.PrismDBMig()); err == nil {
		mig = paths.PrismDBMig()
	}
	s, err := New(t.TempDir()+"/prism.db", mig, fe, func(string, any) {})
	if err != nil {
		t.Fatal(err)
	}
	s.refreshDevices()
	devs := s.connected()
	c := &configRow{ID: "c1", Name: "Test", Data: parseConfig(colorShift)}
	s.db.Exec(`INSERT INTO configs (id, name, config_json, schema, is_current) VALUES ('c1', 'Test', '{}', 1, 1)`)

	// The colour shift moves.
	a := renderZones(&c.Data, devs, 0)
	b := renderZones(&c.Data, devs, 1000)
	kb := devs[0].Zones[0].UniqueID
	if a[kb] == b[kb] {
		t.Errorf("colour shift did not move: %v", a[kb])
	}

	// A single colour on one key only.
	// As the client sends it.
	body := []byte(`{"configurationUpdateData":{"defaultColor":{"red":0,"green":255,"blue":0},"metadata":{"effectType":"SINGLE_COLOR"},"dimFactorOnMonitorSleep":0},
		"selectionInfo":{"deviceSelection":{"mode":"crossDevice"},"zoneSelection":{"zoneIds":["` + kb + `"],"mode":"ids"}}}`)
	ups, err := parseUpdates(body)
	if err != nil {
		t.Fatal(err)
	}
	s.applyUpdates(c, modeBase, ups, parseSelection(body))
	got := renderZones(&c.Data, devs, 1000)
	if got[kb] != (Color{0, 255, 0}) {
		t.Errorf("selected key = %v", got[kb])
	}
	other := devs[0].Zones[1].UniqueID
	if got[other] == (Color{0, 255, 0}) {
		t.Error("the colour spread to other keys")
	}
	zones, graphics := frontendZones(&c.Data, devs)
	if len(zones) != 3 || len(graphics) != 1 || zones[kb].BaseDefaultColor != (Color{0, 255, 0}) || len(zones[other].BaseFoundationGraphicIDs) != 1 {
		t.Errorf("frontend: %d zones %d graphics %+v", len(zones), len(graphics), zones[kb])
	}
	// Everything selected: the global configuration, overrides gone.
	ups, _ = parseUpdates([]byte(`{"defaultColor":{"red":9,"green":9,"blue":9},"metadata":{"effectType":"OFF"}}`))
	s.applyUpdates(c, modeBase, ups, nil)
	if len(c.Data.Base.DeviceConfigs) != 0 || renderZones(&c.Data, devs, 0)[other] != (Color{9, 9, 9}) {
		t.Errorf("global update: %+v", c.Data.Base)
	}
	if b, _ := json.Marshal(c.Data); len(b) == 0 {
		t.Error("config does not marshal")
	}
}

func BenchmarkRender(b *testing.B) {
	devs := []*device{}
	for _, e := range testDevices() {
		devs = append(devs, newDevice(e, nil, false))
	}
	// A full keyboard's worth of zones.
	for i := 0; i < 104; i++ {
		devs[0].Zones = append(devs[0].Zones, zone{UniqueID: itoa64(int64(i)), AdjustedX: uint16(31000 + i*8), AdjustedY: 30100})
	}
	c := parseConfig(colorShift)
	for i := 0; i < b.N; i++ {
		renderZones(&c, devs, int64(i*33))
	}
}

func itoa64(i int64) string { return json.Number(fmt.Sprint(i)).String() }

// A Fade reaction, as the client builds it (isLocal, a single colour,
// row wave with no spread, and an aperture fading it out).
const fadeReactive = `{"base":{"globalConfig":{"defaultColor":{"red":0,"green":0,"blue":0},"graphics":[],"foundation":{}}},
"idle":{"globalConfig":{"defaultColor":{"red":0,"green":0,"blue":40},"graphics":[],"foundation":{}}},"idleTimeMs":60000,
"reactive":{"globalConfig":{"triggerConfig":{"isLocal":true,"graphicId":"r1"},"graphics":[{"type":0,"zIndex":0,"uniqueId":"r1","hasForeground":true,
"foreground":{"duration":1000,"spatialWavelength":0,"phaseMatrixShape":2,"repetitions":1,"origin":{"x":0,"y":0},"isOriginFixed":false,
"gradient":{"colors":[{"position":0,"color":{"red":255,"green":255,"blue":255}}]}},"hasAperture":true,
"aperture":{"duration":1000,"spatialWavelength":0,"repetitions":1,"origin":{"x":0,"y":0},"isOriginFixed":false,
"gradient":{"initialMask":{"opacity":255,"brightness":255},"sections":[{"finalMask":{"opacity":0,"brightness":0},"length":255}]},"phaseMatrixShape":2}}],"foundation":{}}}}`

func TestReactiveAndIdle(t *testing.T) {
	fe := &fakeEngine{devs: testDevices(), frames: map[int][]devicert.PrismWrite{}}
	s, err := New(t.TempDir()+"/prism.db", "", fe, func(string, any) {})
	if err != nil {
		t.Fatal(err)
	}
	s.refreshDevices()
	devs := s.connected()
	c := &configRow{ID: "r", Data: parseConfig(fadeReactive)}
	if !usesInput(&c.Data) {
		t.Fatal("a reactive layer needs input")
	}
	s.mu.Lock()
	s.current = c
	s.mu.Unlock()
	// The keyboard's A key (HID 4) is pressed.
	s.onInput(engine.PrismInput{DeviceID: 159, Pressed: []uint8{4}})
	colors := renderZones(&ConfigData{Base: c.Data.Base}, devs, 0)
	s.react(colors, devs)
	a, other := devs[0].Zones[0].UniqueID, devs[0].Zones[1].UniqueID
	if colors[a] == (Color{}) || colors[other] != (Color{}) {
		t.Errorf("after the press: key %v, other key %v", colors[a], colors[other])
	}
	// Idle once the idle time passes without input.
	if g := s.layerFor(&c.Data, time.Now().Add(-2*time.Minute)); g != &c.Data.Idle {
		t.Error("should be idle")
	}
	if g := s.layerFor(&c.Data, time.Now()); g != &c.Data.Base {
		t.Error("should be active")
	}
}

// Dragging a device on the canvas moves it by the drag, and the answer
// is the device where it now is (the canvas reads its new translation).
func TestMoveDevice(t *testing.T) {
	fe := &fakeEngine{devs: testDevices(), frames: map[int][]devicert.PrismWrite{}}
	mig := ""
	if _, err := os.Stat(paths.PrismDBMig()); err == nil {
		mig = paths.PrismDBMig()
	}
	s, err := New(t.TempDir()+"/prism.db", mig, fe, func(string, any) {})
	if err != nil {
		t.Fatal(err)
	}
	s.refreshDevices()
	before := *s.connected()[0]
	moved := s.moveDevice(before.ID, 30, -20)
	if moved == nil {
		t.Fatal("no device")
	}
	if dx, dy := moved.CanvasTranslationX-before.CanvasTranslationX, moved.CanvasTranslationY-before.CanvasTranslationY; dx < 29 || dx > 31 || dy < -21 || dy > -19 {
		t.Errorf("moved by (%d, %d), want (30, -20)", dx, dy)
	}
	again := s.moveDevice(before.ID, 0, 0)
	if again.CanvasTranslationX != moved.CanvasTranslationX || again.CanvasTranslationY != moved.CanvasTranslationY {
		t.Errorf("a zero drag moved it: %d,%d -> %d,%d", moved.CanvasTranslationX, moved.CanvasTranslationY, again.CanvasTranslationX, again.CanvasTranslationY)
	}
	if s.moveDevice(999, 1, 1) != nil {
		t.Error("moved a device that isn't there")
	}
}
