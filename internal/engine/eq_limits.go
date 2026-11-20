package engine

import (
	"net/http"

	"ggl/internal/httpx"
)

// eqLimit is how far a headset's own EQ goes, as GG 124's Engine lists it
// per device (deviceEQLimitation). GG's device EQ page clamps game presets
// to the gain range, sends a filter that is off with the disabled values,
// and hides the presets the device can't play. Values left out are null.
type eqLimit struct {
	MaxGain            int      `json:"maxGain"`
	MinGain            int      `json:"minGain"`
	DisabledFrequency  int      `json:"disabledFrequency"`
	DisabledQFactor    *float64 `json:"disabledQFactor"`
	DisabledFilterType *int     `json:"disabledFilterType"`
	DisabledGains      *int     `json:"disabledGains"`
	UnsupportedEq      []string `json:"unsupportedEq"`
}

// eqLimits are by Engine device id, as the Engine keeps them.
var eqLimits = func() map[int]eqLimit {
	gain := func(g int) eqLimit { return eqLimit{MaxGain: g, MinGain: -g, DisabledFrequency: 20001} }
	q, zero := 1.414, 0
	gen2 := gain(12)
	gen2.DisabledQFactor, gen2.DisabledFilterType, gen2.DisabledGains = &q, &zero, &zero
	elite := gain(12)
	elite.UnsupportedEq = []string{ // its two "Walkie Talkie" presets
		"4cb0fad9-34b6-4a3a-9a8f-0e210cfd57a1",
		"726cc928-eb01-4d8f-b0e9-d594eb19fc37",
	}
	return map[int]eqLimit{
		308: gain(10), // Arctis Nova 5
		310: gain(10), // Arctis Nova 5X
		340: gain(10), // Arctis Nova 5X White
		316: elite,    // Arctis Nova Elite
		324: gain(12), // Arctis GameBuds
		326: gain(12), // Arctis GameBuds X
		353: gain(12), // Arctis Nova 3 Wireless
		355: gain(12), // Arctis Nova 3X Wireless
		364: gain(12), // Arctis Nova Pro Omni
		358: gen2,     // Arctis Nova 7 Gen 2
		360: gen2,     // Arctis Nova 7P Gen 2
		362: gen2,     // Arctis Nova 7X Gen 2
	}
}()

func (e *Engine) eqLimitRoutes() {
	e.srv.Handle("GET /parametricEQ/{id}/deviceEQLimitation", func(w http.ResponseWriter, r *http.Request) {
		var out *eqLimit
		if l, ok := eqLimits[pathInt(r, "id")]; ok {
			out = &l
		}
		httpx.JSON(w, map[string]any{"deviceEQLimitation": out})
	})
}
