package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEQBandsKeepBandOrder(t *testing.T) {
	var d gameEQData
	for i := range d {
		d[i] = eqFilter{Enabled: true, Frequency: float64(int(31) << i), FilterType: "peakingEQ"}
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	// The client reads bands with Object.values, so filter10 must come last.
	if i, j := strings.Index(string(b), `"filter2"`), strings.Index(string(b), `"filter10"`); i < 0 || j < i {
		t.Fatalf("bands out of order: %s", b)
	}
	var back gameEQData
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back != d {
		t.Fatalf("round trip changed bands: %+v", back)
	}
}

func TestCustomEQBandsFromClient(t *testing.T) {
	var d customEQData
	in := `{"eq_band_1":{"enabled":1,"filter_type":4,"frequency":80,"gain":-3,"q_factor":0.7},"eq_band_10":{"enabled":0,"filter_type":1,"frequency":16000,"gain":0,"q_factor":1}}`
	if err := json.Unmarshal([]byte(in), &d); err != nil {
		t.Fatal(err)
	}
	if d[0].FilterType != 4 || d[0].Gain != -3 || d[9].Frequency != 16000 {
		t.Fatalf("decoded %+v", d)
	}
}
