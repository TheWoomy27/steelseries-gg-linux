package prism

import (
	"encoding/json"
	"testing"
)

const reflectLeft = `{"reflectData":{"samplingArea":8}}`

// The bodies GG's Prism UI sends for each effect (render/249.js): only
// Reflect and Audio Visualizer carry uniqueEffectType; the plain effects
// send metadata without it and must end a unique effect left on the zone.
func TestEffectUpdatesSetUniqueEffect(t *testing.T) {
	const graphic = `"graphics":[{"type":0,"zIndex":0,"uniqueId":"g","hasForeground":true,"foreground":{"duration":3000,"gradient":{"colors":[{"red":255,"green":0,"blue":0,"position":0}]}}}],"foundations":[{"origin":{"x":0,"y":0},"opacity":255,"graphicId":"g"}]`
	cases := []struct {
		name     string
		body     string
		wantType int
		wantData string
	}{
		{"off", `{"defaultColor":{"red":0,"green":0,"blue":0},"metadata":{"effectType":"OFF"}}`, 0, `{}`},
		{"solid color", `{"defaultColor":{"red":0,"green":0,"blue":255},"metadata":{"effectType":"SINGLE_COLOR"}}`, 0, `{}`},
		{"breathe", `{` + graphic + `,"metadata":{"effectType":"BREATHE"}}`, 0, `{}`},
		{"color shift", `{` + graphic + `,"metadata":{"effectType":"COLOR_SHIFT"}}`, 0, `{}`},
		{"reflect", `{"metadata":{"effectType":"REFLECT"},"uniqueEffectType":1,"uniqueEffectData":{"reflectData":{"samplingArea":2}}}`, effectReflect, `{"reflectData":{"samplingArea":2}}`},
		{"audio visualizer", `{"metadata":{"effectType":"AUDIO_VISUALIZER"},"uniqueEffectType":4,"uniqueEffectData":{"audioVisualizerData":{"colorProfileId":1}}}`, effectAudioVisualizer, `{"audioVisualizerData":{"colorProfileId":1}}`},
	}
	for _, c := range cases {
		s := Single{UniqueEffectType: effectReflect, UniqueEffectData: json.RawMessage(reflectLeft)}
		ups, err := parseUpdates([]byte(`{"configurationUpdateData":` + c.body + `}`))
		if err != nil || len(ups) != 1 {
			t.Fatalf("%s: parse: %v %d", c.name, err, len(ups))
		}
		ups[0].apply(&s)
		if s.UniqueEffectType != c.wantType || string(s.UniqueEffectData) != c.wantData {
			t.Errorf("%s: uniqueEffect = %d %s, want %d %s", c.name, s.UniqueEffectType, s.UniqueEffectData, c.wantType, c.wantData)
		}
	}
}

// An update without metadata (only a colour or a monitor-sleep dim) keeps
// the zone's unique effect.
func TestUpdateWithoutMetadataKeepsUniqueEffect(t *testing.T) {
	for _, body := range []string{
		`{"defaultColor":{"red":1,"green":2,"blue":3}}`,
		`{"dimFactorOnMonitorSleep":50}`,
	} {
		s := Single{UniqueEffectType: effectReflect, UniqueEffectData: json.RawMessage(reflectLeft)}
		ups, _ := parseUpdates([]byte(`{"configurationUpdateData":` + body + `}`))
		ups[0].apply(&s)
		if s.UniqueEffectType != effectReflect || string(s.UniqueEffectData) != reflectLeft {
			t.Errorf("%s: uniqueEffect = %d %s", body, s.UniqueEffectType, s.UniqueEffectData)
		}
	}
}
