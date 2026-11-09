package engine

import (
	"encoding/json"
	"testing"
)

func TestFlattenEQ(t *testing.T) {
	var peq map[string]any
	json.Unmarshal([]byte(`{"name": "Bass Boost", "preset_type": 1, "eq_data": {
		"eq_band_1": {"enabled": 1, "filter_type": 4, "frequency": 80, "gain": 6, "q_factor": 0.7},
		"eq_band_2": {"enabled": 0, "filter_type": 1, "frequency": 1000, "gain": -3, "q_factor": 1.4}}}`), &peq)
	if eqIsFlat(peq) {
		t.Fatal("a 6 dB low shelf is not flat")
	}
	flat := flatEQ(peq)
	if !eqIsFlat(flat) || flat["name"] != "Flat" {
		t.Errorf("flattened: %v", flat)
	}
	band := flat["eq_data"].(map[string]any)["eq_band_1"].(map[string]any)
	if band["frequency"] != 80.0 || band["filter_type"] != 4.0 {
		t.Errorf("flattening changed the band: %v", band)
	}
	if g := peq["eq_data"].(map[string]any)["eq_band_1"].(map[string]any)["gain"]; g != 6.0 {
		t.Errorf("flattening changed the original: gain %v", g)
	}
	// A disabled band's gain does not count.
	json.Unmarshal([]byte(`{"eq_data": {"eq_band_1": {"enabled": 0, "gain": 4}}}`), &peq)
	if !eqIsFlat(peq) {
		t.Error("a disabled band made the EQ not flat")
	}
}
