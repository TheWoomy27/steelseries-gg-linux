package prism

import (
	"encoding/json"
	"math"
	"testing"
)

// A keyboard grid 10 keys wide and 6 high, as Prism maps it.
func gridZone(x, y uint8) *zone {
	return &zone{BitmapX: x, BitmapY: y, DeviceHasBitmapCoordinates: true, DeviceBitmapXMin: 0, DeviceBitmapXSize: 10, DeviceBitmapYMax: 5, DeviceBitmapYSize: 6}
}

func TestVisualizerZones(t *testing.T) {
	f := &avFrame{bands: map[int][]float32{10: {0.5, 0, 0, 0, 0, 0, 0, 0, 0, 1}}, level: 0.5}
	for _, c := range []struct {
		z    *zone
		want int
	}{
		{gridZone(0, 5), avLow},        // bottom row, under the band's level
		{gridZone(0, 3), avMid},        // a third up: 2/6 is under 0.5
		{gridZone(0, 2), avBackground}, // half way up: 3/6 is not under 0.5
		{gridZone(9, 0), avHigh},       // the top of a full band
		{gridZone(1, 5), avBackground}, // a silent band
		{&zone{BitmapX: 0, BitmapY: 5, DeviceHasBitmapCoordinates: true, DeviceBitmapXSize: 7, DeviceBitmapYMax: 5, DeviceBitmapYSize: 6}, avNone}, // no bands for its width
		{&zone{DeviceHasLinearIndexing: true, LinearIndex: 1, DeviceLinearSize: 3}, avMid},                                                         // 1/3 under 0.5, coloured by the level
		{&zone{DeviceHasLinearIndexing: true, LinearIndex: 2, DeviceLinearSize: 3}, avBackground},                                                  // 2/3 above it
		{&zone{}, avMid}, // a single zone: the level's colour
	} {
		if got := avWhich(f, c.z); got != c.want {
			t.Errorf("%+v: %d, want %d", c.z, got, c.want)
		}
	}
	dark := profileOf(&Single{UniqueEffectData: json.RawMessage(`{"audioVisualizerData":{"colorProfileId":"AUDIOVISUALIZER_COLOR_PROFILE_DARK"}}`)})
	if got := avColor(f, gridZone(1, 5), dark); got != (Color{32, 32, 32}) {
		t.Errorf("dark background %v", got)
	}
	// GG sends the profile by number.
	darkBlind := profileOf(&Single{UniqueEffectData: json.RawMessage(`{"audioVisualizerData":{"colorProfileId":3}}`)})
	if got := avColor(f, gridZone(1, 5), darkBlind); got != (Color{32, 32, 32}) {
		t.Errorf("dark deuteranopia background %v", got)
	}
	if got := avColor(f, gridZone(9, 0), darkBlind); got != (Color{255, 0, 255}) {
		t.Errorf("dark deuteranopia high %v", got)
	}
	if got := avColor(f, gridZone(0, 5), profileOf(&Single{})); got != (Color{2, 255, 140}) {
		t.Errorf("light low %v", got)
	}
}

func TestSpectrumAndBands(t *testing.T) {
	s := make([]float64, fftSize)
	for i := range s {
		s[i] = math.Sin(2 * math.Pi * 1000 * float64(i) / audioRate)
	}
	mags := spectrum(s)
	bin := int(math.Floor(1000.0 / (audioRate / float64(fftSize))))
	if m := max(mags[bin], mags[bin+1]); m < 0.8 || m > 1.05 {
		t.Errorf("a full-scale 1 kHz sine reads %.2f", m)
	}
	b := bands(mags, 10)
	loudest := 0
	for i := range b {
		if b[i] > b[loudest] {
			loudest = i
		}
	}
	// 1 kHz is in band 5 of 10 log-spaced bands from 40 Hz to 16 kHz.
	if loudest != 5 || b[5] < 0.95 || b[0] > 0.3 {
		t.Errorf("bands %v", b)
	}
}

func TestNormalise(t *testing.T) {
	var a audioCapture
	var f *avFrame
	for i := 0; i < 20; i++ {
		f = a.normalise(map[int][]float32{4: {0.2, 0.5, 0.8, 0.9}}, 0.2, 0.9, 0.5, 0.5, 3)
	}
	b := f.bands[4]
	if !(b[0] < b[1] && b[1] < b[2] && b[2] <= b[3]) || b[0] > 0.1 || b[3] < 0.8 || b[3] > 1.2 {
		t.Errorf("normalised %v (low %.2f high %.2f mult %.2f)", b, a.low, a.high, a.mult)
	}
	if f.level <= 0 || f.level > 0.6 {
		t.Errorf("level %v", f.level)
	}
	if p := previewFrame([]int{10}, 0); p.bands[10][0] == previewFrame([]int{10}, 500).bands[10][0] {
		t.Error("the preview does not move")
	}
}
