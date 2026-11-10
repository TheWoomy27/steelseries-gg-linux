package prism

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestReflectZoneRect(t *testing.T) {
	key := func(x uint8) *zone {
		return &zone{DeviceType: 0, DeviceHasBitmapCoordinates: true, BitmapX: x, DeviceBitmapXMin: 2, DeviceBitmapXSize: 4}
	}
	for _, c := range []struct {
		name string
		z    *zone
		area int
		want rect
	}{
		{"keyboard default: its column of the bottom", key(3), areaDefault, rect{0.25, 0.67, 0.5, 1}},
		{"keyboard, all: its column", key(5), areaAll, rect{0.75, 0, 1, 1}},
		{"keyboard, left: the left third", key(5), areaLeft, rect{0, 0, 0.33, 1}},
		{"keyboard, a corner", key(5), areaTopRight, rect{0.5, 0, 1, 0.5}},
		{"mouse default: bottom right", &zone{DeviceType: 1}, areaDefault, rect{0.5, 0.5, 1, 1}},
		{"headset default: top", &zone{DeviceType: 3}, areaDefault, rect{0, 0, 1, 0.33}},
		{"left speaker", &zone{DeviceType: 11, X: 0}, areaDefault, rect{0, 0, 0.33, 1}},
		{"right speaker", &zone{DeviceType: 11, X: 1}, areaDefault, rect{0.67, 0, 1, 1}},
		{"mousepad default: all", &zone{DeviceType: 2}, areaDefault, rect{0, 0, 1, 1}},
		{"no area in the data", &zone{DeviceType: 1}, 0, rect{0.5, 0.5, 1, 1}},
	} {
		if got := zoneRect(c.z, c.area); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestReflectSamplingArea(t *testing.T) {
	for data, want := range map[string]int{
		`{"reflectData":{"samplingArea":7}}`:                           7,
		`{"reflectData":{"samplingArea":"SAMPLING_AREA_BOTTOM_LEFT"}}`: areaBottomLeft,
		`{"reflectData":{}}`:                                           areaDefault,
		`{}`:                                                           areaDefault,
		``:                                                             areaDefault,
	} {
		s := &Single{UniqueEffectType: effectReflect, UniqueEffectData: json.RawMessage(data)}
		if got := samplingArea(s); got != want {
			t.Errorf("%s: %d, want %d", data, got, want)
		}
	}
}

func TestReflectSaturate(t *testing.T) {
	near := func(a, b [3]float64) bool {
		for i := range a {
			if math.Abs(a[i]-b[i]) > 0.01 {
				return false
			}
		}
		return true
	}
	// Greys stay grey; a dull blue becomes a nearly pure one as bright.
	if got := saturate([3]float64{0.4, 0.4, 0.4}); !near(got, [3]float64{0.4, 0.4, 0.4}) {
		t.Errorf("grey: %v", got)
	}
	got := saturate([3]float64{0.3, 0.35, 0.6})
	if math.Abs(got[2]-0.6) > 0.001 || got[0] > 0.05 || got[1] > got[2] {
		t.Errorf("dull blue: %v", got)
	}
	for _, c := range [][3]float64{{1, 0, 0}, {0.2, 0.7, 0.1}, {0.9, 0.1, 0.8}, {0, 0, 0}} {
		h, s, v := rgbToHSV(c[0], c[1], c[2])
		r, g, b := hsvToRGB(h, s, v)
		if !near([3]float64{r, g, b}, c) {
			t.Errorf("HSV round trip of %v: %v %v %v", c, r, g, b)
		}
	}
}

func TestReflectAverageAndSmoothing(t *testing.T) {
	// A screen red on the left half, blue on the right.
	s := &screen{w: 4, h: 2, pix: make([]float64, 4*2*3)}
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			i := (y*4 + x) * 3
			if x < 2 {
				s.pix[i] = 1
			} else {
				s.pix[i+2] = 1
			}
		}
	}
	if got := s.average(rect{0, 0, 0.5, 1}); got != [3]float64{1, 0, 0} {
		t.Errorf("left half: %v", got)
	}
	if got := s.average(rect{0, 0, 1, 1}); got != [3]float64{0.5, 0, 0.5} {
		t.Errorf("all: %v", got)
	}

	z := &zone{DeviceType: 1} // bottom right: blue
	cfg := &Single{UniqueEffectType: effectReflect}
	smooth := map[rect]smoothed{}
	start := time.Now()
	f := &reflectFrame{scr: s, now: start, smooth: smooth, colours: map[rect]Color{}}
	if got := f.color(z, cfg); got != (Color{0, 0, 255}) {
		t.Fatalf("first frame: %v", got)
	}
	// The screen turns red: 50 ms later the zone is part way there, and
	// 50 ms after that (with no new capture) further still.
	red := &screen{w: 1, h: 1, pix: []float64{1, 0, 0}}
	k := math.Exp(-0.5)
	for i, want := range []Color{
		{uint8(255*(1-k) + 0.5), 0, uint8(255*k + 0.5)},
		{uint8(255*(1-k*k) + 0.5), 0, uint8(255*k*k + 0.5)},
	} {
		f = &reflectFrame{scr: red, now: start.Add(time.Duration(i+1) * 50 * time.Millisecond), smooth: smooth, colours: map[rect]Color{}}
		if got := f.color(z, cfg); got != want {
			t.Errorf("after %d ms: %v, want %v", (i+1)*50, got, want)
		}
	}
	// Previews have no history.
	f = &reflectFrame{scr: red, colours: map[rect]Color{}}
	if got := f.color(z, cfg); got != (Color{255, 0, 0}) {
		t.Errorf("preview: %v", got)
	}
}

func TestReflectPreview(t *testing.T) {
	// The preview picture gives zones colour, different in different places.
	f := &reflectFrame{scr: previewScreen(), colours: map[rect]Color{}}
	cfg := &Single{UniqueEffectType: effectReflect, UniqueEffectData: json.RawMessage(`{"reflectData":{"samplingArea":2}}`)}
	left := f.color(&zone{}, cfg)
	cfg.UniqueEffectData = json.RawMessage(`{"reflectData":{"samplingArea":3}}`)
	right := f.color(&zone{}, cfg)
	if left == right || left == (Color{}) {
		t.Errorf("left %v, right %v", left, right)
	}
}
