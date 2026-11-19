package engine

import (
	"maps"
	"strconv"
	"sync"

	"github.com/steelseries/golisp"

	"ggl/internal/devicert"
	"ggl/internal/oled"
)

// The Engine app GG calls Sensei/Rival Pro Display ("shihan monitoring"
// in the Engine): a Sensei or Rival Pro mouse's battery, its base
// station's, its DPI and polling rate, on the screen of a keyboard or
// headset base whose spec can show them (write-shihan-monitoring-data:
// the 2024 Apex Pro keyboards, the Arctis Nova Pro bases). The mouse's
// spec reports the readings (shihanMonitoringEngineApp events). GG lists
// the app in Engine's Apps once such a mouse has connected; it starts
// off, and while on it has the screens over GameSense.

const proDisplayName = "Sensei/Rival Pro Display"

type proDisplayState struct {
	mu       sync.Mutex
	loaded   bool
	on       bool
	readings map[string]int // as the spec names them: MouseBatt, BaseBatt, DpiX, DpiY, Poll, Sensor
}

// proDisplayMouse reports whether a device's spec feeds the display.
func proDisplayMouse(d *devicert.Decl) bool {
	return d != nil && d.HasFunction("handle_update_engine_app_DPI")
}

func (e *Engine) proDisplayOn() bool {
	p := &e.proDisplay
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.loaded {
		e.state.Get("proDisplay", &p.on)
		p.loaded = true
	}
	return p.on
}

func (e *Engine) setProDisplay(on bool) {
	p := &e.proDisplay
	p.mu.Lock()
	p.on, p.loaded = on, true
	p.mu.Unlock()
	e.state.Set("proDisplay", on)
}

// proDisplayStatus is the app's state as GG reads it.
func (e *Engine) proDisplayStatus() map[string]any {
	var seen bool
	e.state.Get("proDisplaySeen", &seen)
	return map[string]any{"enabled": e.proDisplayOn(), "hidden": !seen}
}

// proDisplayReadings takes readings the mouse's spec reports.
func (e *Engine) proDisplayReadings(m map[string]any) {
	p := &e.proDisplay
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.readings == nil {
		p.readings = map[string]int{}
	}
	for k, v := range m {
		if n, ok := number(v); ok {
			p.readings[k] = n
		}
	}
}

// proDisplayMouseReady shows the app in GG the first time such a mouse
// connects, and takes the readings the mouse only reports when they
// change: DPI and polling rate from its configuration, which the mouse
// has once connected, and the batteries, which its spec reads.
func (e *Engine) proDisplayMouseReady(l *liveDevice, d *devicert.Decl) {
	var seen bool
	e.state.Get("proDisplaySeen", &seen)
	if !seen {
		e.state.Set("proDisplaySeen", true)
		e.emit("SENSEI_RIVAL_PRO_DISPLAY_UNHIDE", map[string]any{"hidden": false})
	}
	if cfg, err := e.activeConfig(l.dbID); err == nil {
		e.proDisplayReadings(configReadings(d, cfg.settingsMap()))
	}
	if d.HasFunction("get_battery_status") {
		d.Call("get_battery_status")
	}
}

// configReadings are the DPI and polling rate of the configuration's
// current levels.
func configReadings(d *devicert.Decl, s map[string]any) map[string]any {
	level := func(api, list string) map[string]any {
		m, _ := s[api].(map[string]any)
		i, _ := number(m["index"])
		levels, _ := m[list].([]any)
		if i < 0 || i >= len(levels) {
			return nil
		}
		lv, _ := levels[i].(map[string]any)
		return lv
	}
	out := map[string]any{}
	if lv := level("batch_xy_resolution", "mouse_sensitivity"); lv != nil {
		out["DpiX"], out["DpiY"] = lv["x_dpi"], lv["y_dpi"]
	}
	// The spec turns its rate codes into Hz.
	hz := func(v any) any {
		n, ok := number(v)
		if !ok {
			return nil
		}
		r, err := d.Call("translate_polling_and_report_rate_to_ui", golisp.IntegerWithValue(int64(n)))
		if err != nil || !golisp.IntegerP(r) {
			return nil
		}
		return golisp.IntegerValue(r)
	}
	if lv := level("batch_polling_and_report_rate", "performance_profile"); lv != nil {
		out["Poll"], out["Sensor"] = hz(lv["polling_rate"]), hz(lv["report_rate"])
	}
	return out
}

// proDisplayPicture is what the display shows now (128x40, packed by
// rows), or nil while it is off or no such mouse is connected.
func (e *Engine) proDisplayPicture() []byte {
	if !e.proDisplayOn() {
		return nil
	}
	e.mu.Lock()
	lives := make([]*liveDevice, 0, len(e.live))
	for _, l := range e.live {
		lives = append(lives, l)
	}
	e.mu.Unlock()
	mouse := false
	for _, l := range lives {
		l.mu.Lock()
		ready := l.ready
		l.mu.Unlock()
		mouse = mouse || ready && proDisplayMouse(l.primary())
	}
	if !mouse {
		return nil
	}
	p := &e.proDisplay
	p.mu.Lock()
	r := maps.Clone(p.readings)
	p.mu.Unlock()
	return proDisplayFrame(r).Pack()
}

// proDisplayFrame lays out the readings: the mouse's battery and its base
// station's (a dongle has none) above, DPI (X/Y when they differ) and
// polling rate below. The mouse reports no battery while it is off.
func proDisplayFrame(r map[string]int) *oled.Canvas {
	mouse := "--"
	if b := r["MouseBatt"]; b > 0 && b <= 100 {
		mouse = strconv.Itoa(b) + "%"
	}
	top := [2]oled.Stat{{Label: "Mouse", Value: mouse}}
	if b := r["BaseBatt"]; b > 0 && b <= 100 {
		top[1] = oled.Stat{Label: "Base", Value: strconv.Itoa(b) + "%"}
	}
	dpi := "--"
	if x := r["DpiX"]; x > 0 {
		dpi = strconv.Itoa(x)
		if y := r["DpiY"]; y > 0 && y != x {
			dpi += "/" + strconv.Itoa(y)
		}
	}
	bottom := [2]oled.Stat{{Label: "DPI", Value: dpi}}
	if hz := r["Poll"]; hz > 0 {
		bottom[1] = oled.Stat{Value: shortHz(hz), Unit: " Hz"}
	}
	return oled.Stats(128, 40, [][2]oled.Stat{top, bottom})
}

// shortHz writes thousands as k (8000 is 8k), as GG's display does.
func shortHz(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return strconv.FormatFloat(float64(n)/1000, 'f', -1, 64) + "k"
}
