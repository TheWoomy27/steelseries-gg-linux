package prism

import (
	"encoding/json"
	"sort"
)

// The Audio Visualizer effect (unique effect 4), as Windows Prism draws
// it: each column of a keyboard's key grid is a frequency band, and a
// key lights once its band is louder than the key's height in the grid;
// keys in the bottom third light in the profile's low colour, the
// middle third in its mid colour, the top in its high colour, and the
// others show the profile's background. Devices without a key grid
// light by overall volume: in a row of zones (linear indexing) as a
// level meter, otherwise all at once in the colour of the volume
// (above 0, a third, two thirds).

const effectAudioVisualizer = 4

// avProfile is a colour profile: low, mid and high colours and the
// background.
type avProfile [4]Color

var avProfiles = map[string]avProfile{
	"AUDIOVISUALIZER_COLOR_PROFILE_LIGHT":              {{2, 255, 140}, {255, 142, 24}, {255, 30, 30}, {255, 255, 255}},
	"AUDIOVISUALIZER_COLOR_PROFILE_DARK":               {{2, 255, 140}, {255, 142, 24}, {255, 30, 30}, {32, 32, 32}},
	"AUDIOVISUALIZER_COLOR_PROFILE_LIGHT_DEUTERANOPIA": {{3, 188, 255}, {255, 212, 0}, {255, 0, 255}, {255, 255, 255}},
	"AUDIOVISUALIZER_COLOR_PROFILE_DARK_DEUTERANOPIA":  {{3, 188, 255}, {255, 212, 0}, {255, 0, 255}, {32, 32, 32}},
}

// avProfileIDs are the profiles by the number GG sends (0 Light, 1
// Dark, 2 and 3 their deuteranopia versions).
var avProfileIDs = []string{
	"AUDIOVISUALIZER_COLOR_PROFILE_LIGHT",
	"AUDIOVISUALIZER_COLOR_PROFILE_DARK",
	"AUDIOVISUALIZER_COLOR_PROFILE_LIGHT_DEUTERANOPIA",
	"AUDIOVISUALIZER_COLOR_PROFILE_DARK_DEUTERANOPIA",
}

// profileOf reads a configuration's colour profile (Light by default):
// GG sends its number, the names are accepted too.
func profileOf(s *Single) avProfile {
	var d struct {
		AudioVisualizerData *struct {
			ColorProfileID any `json:"colorProfileId"`
		} `json:"audioVisualizerData"`
	}
	if json.Unmarshal(s.UniqueEffectData, &d) == nil && d.AudioVisualizerData != nil {
		switch id := d.AudioVisualizerData.ColorProfileID.(type) {
		case float64:
			if i := int(id); i >= 0 && i < len(avProfileIDs) {
				return avProfiles[avProfileIDs[i]]
			}
		case string:
			if p, ok := avProfiles[id]; ok {
				return p
			}
		}
	}
	return avProfiles[avProfileIDs[0]]
}

// Which colour a zone shows (processing.WhichColor).
const (
	avNone = iota
	avBackground
	avLow
	avMid
	avHigh
)

// avWhich is Prism's zone mapping and ProcessRegions for one zone.
func avWhich(f *avFrame, z *zone) int {
	byLevel := func() int {
		switch {
		case f.level > 0.66:
			return avHigh
		case f.level > 0.33:
			return avMid
		case f.level > 0:
			return avLow
		}
		return avBackground
	}
	switch {
	case z.DeviceHasBitmapCoordinates:
		x := z.BitmapX - z.DeviceBitmapXMin
		fromBottom := z.DeviceBitmapYMax - z.BitmapY
		h := z.DeviceBitmapYSize
		third := uint8(uint32(h) * 342 >> 10)
		assigned := avHigh
		if third > fromBottom {
			assigned = avLow
		} else if h-third > fromBottom {
			assigned = avMid
		}
		if h == 0 {
			return avNone
		}
		start := float32(fromBottom)/float32(h) + 1e-5
		b, ok := f.bands[int(z.DeviceBitmapXSize)]
		if !ok || int(x) >= len(b) {
			return avNone
		}
		if b[x] > start {
			return assigned
		}
		return avBackground
	case z.DeviceHasLinearIndexing:
		if z.DeviceLinearSize == 0 {
			return byLevel()
		}
		start := float32(z.LinearIndex-z.DeviceLinearMin)/float32(z.DeviceLinearSize) + 1e-5
		if start > f.level {
			return avBackground
		}
		return byLevel()
	}
	return byLevel()
}

// avColor is a zone's colour in a frame.
func avColor(f *avFrame, z *zone, p avProfile) Color {
	switch avWhich(f, z) {
	case avLow:
		return p[0]
	case avMid:
		return p[1]
	case avHigh:
		return p[2]
	}
	return p[3]
}

// usesVisualizer reports whether a layer shows the Audio Visualizer.
func usesVisualizer(g *Group) bool {
	if g.GlobalConfig.UniqueEffectType == effectAudioVisualizer {
		return true
	}
	for i := range g.DeviceTypeConfigs {
		if g.DeviceTypeConfigs[i].Config.UniqueEffectType == effectAudioVisualizer {
			return true
		}
	}
	for i := range g.PerKeyConfigs {
		if g.PerKeyConfigs[i].Config.UniqueEffectType == effectAudioVisualizer {
			return true
		}
	}
	for i := range g.DeviceConfigs {
		if g.DeviceConfigs[i].Config.UniqueEffectType == effectAudioVisualizer {
			return true
		}
	}
	return false
}

// gridWidths are the key grid widths of devices, the band counts the
// effect needs.
func gridWidths(devs []*device) []int {
	seen := map[int]bool{}
	var out []int
	for _, d := range devs {
		for i := range d.Zones {
			z := &d.Zones[i]
			if w := int(z.DeviceBitmapXSize); z.DeviceHasBitmapCoordinates && w > 0 && !seen[w] {
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	sort.Ints(out)
	return out
}

// visualizerSettings are the user's amplitude scale (high, medium, low)
// and output.
func (s *Service) visualizerSettings() (scalar float32, output string) {
	get := func(key string) string {
		var v string
		if s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v) != nil {
			return ""
		}
		var x string
		if json.Unmarshal([]byte(v), &x) == nil {
			return x
		}
		return v
	}
	switch get("audioVisualizerAmplitude") {
	case "low":
		scalar = 0.5
	case "medium":
		scalar = 1.25
	default:
		scalar = 3
	}
	return scalar, get("audioVisualizerPreferredRenderVAD")
}
