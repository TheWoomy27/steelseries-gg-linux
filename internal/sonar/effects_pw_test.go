package sonar

import (
	"encoding/json"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMicEffectsOnPipeWire runs the microphone's effects and chat's
// ClearCast on a private PipeWire instance (see newPWTest), measuring the
// processed microphone and the headset.
func TestMicEffectsOnPipeWire(t *testing.T) {
	for _, pl := range []*ladspa{deepFilterMono, deepFilterStereo, lspGate, lspAutogain} {
		if pluginPath(pl) == "" {
			t.Skipf("%s (%s) is not installed", pl.label, pl.lib)
		}
	}
	p := newPWTest(t)
	wav := func(name string, write func(string)) string {
		path := filepath.Join(p.tmp, name+".wav")
		write(path)
		return path
	}
	const d = 3 * time.Second
	tone := wav("tone", func(f string) { writeTone(t, f, d) })
	quiet := wav("quiet", func(f string) { writeNoise(t, f, d, -60, false) })
	speech := wav("speech", func(f string) { writeNoise(t, f, d, -60, true) })
	noise := wav("noise", func(f string) { writeNoise(t, f, d, -30, false) })
	mic := func(wav string) float64 { return p.level("sonar_mic", "test_headset_mic", wav) }
	set := func(vad string, kv ...any) {
		t.Helper()
		p.editConfig(vad, func(data map[string]any) {
			for i := 0; i < len(kv); i += 2 {
				setPath(data, kv[i].(string), kv[i+1])
			}
		})
	}

	set("chatCapture", "globalEnableState", true, "parametricEQ.enabled", false,
		"noiseCancelingState.enabled", false, "noiseReductionState.enabled", false, "impactNoiseReductionState.enabled", false,
		"noiseGateState.enabled", false, "automaticNoiseGateState.enabled", false, "volumeStabilizerState.enabled", false)
	plain := mic(tone)
	t.Logf("microphone: %.1f dBFS", plain)

	set("chatCapture", "volumeStabilizerState.enabled", true, "volumeStabilizerState.value", 1.0)
	if got := mic(tone); got < plain+10 {
		t.Errorf("volume stabilizer at 1: %.1f dBFS, want 10 dB over %.1f", got, plain)
	} else {
		t.Logf("volume stabilizer at 1: %.1f dBFS", got)
	}
	set("chatCapture", "volumeStabilizerState.enabled", false)

	set("chatCapture", "noiseGateState.enabled", true, "noiseGateState.value", -10.0)
	p.silent("tone under the gate's threshold", mic(tone))
	set("chatCapture", "noiseGateState.value", -60.0)
	p.near("tone over the gate's threshold", mic(tone), plain)

	// The automatic gate learns the floor from noise at -60 dBFS, -54 at the
	// mono microphone, which takes both channels (its 40 ms peaks are about
	// 11 dB higher), then closes on the noise alone and opens for the tone
	// over it.
	set("chatCapture", "automaticNoiseGateState.enabled", true)
	mic(quiet)
	if f := p.s.floor.get(); !(f > -48 && f < -38) {
		t.Errorf("noise floor %.1f dBFS, want about -43", f)
	} else {
		t.Logf("noise floor: %.1f dBFS", f)
	}
	p.silent("noise under the automatic gate", mic(quiet))
	p.near("tone over the noise, through the automatic gate", mic(speech), plain)
	set("chatCapture", "noiseGateState.enabled", false, "automaticNoiseGateState.enabled", false)
	if f := p.s.floor.get(); !math.IsNaN(f) {
		t.Errorf("the noise floor is still measured with the automatic gate off")
	}

	// White noise is what DeepFilterNet removes.
	open := mic(noise)
	t.Logf("noise: %.1f dBFS", open)
	set("chatCapture", "noiseCancelingState.enabled", true, "noiseCancelingState.value", 1.0)
	if got := mic(noise); got > open-15 {
		t.Errorf("ClearCast: noise at %.1f dBFS, want 15 dB under %.1f", got, open)
	} else {
		t.Logf("ClearCast: noise at %.1f dBFS", got)
	}
	// Noise reduction runs only without ClearCast, holding noise at most
	// noiseReductionDB down.
	set("chatCapture", "noiseCancelingState.enabled", false, "noiseReductionState.enabled", true, "noiseReductionState.value", 1.0)
	if got := mic(noise); got > open-6 || got < open-noiseReductionDB-2 {
		t.Errorf("noise reduction: noise at %.1f dBFS, want 6 to %g dB under %.1f", got, noiseReductionDB, open)
	} else {
		t.Logf("noise reduction: noise at %.1f dBFS", got)
	}

	set("chatRender", "globalEnableState", true, "noiseCancelingState.enabled", false)
	chat := p.level("test_head", "sonar_chat", noise)
	set("chatRender", "noiseCancelingState.enabled", true, "noiseCancelingState.value", 1.0)
	if got := p.level("test_head", "sonar_chat", noise); got > chat-15 {
		t.Errorf("chat ClearCast: noise at %.1f dBFS, want 15 dB under %.1f", got, chat)
	} else {
		t.Logf("chat ClearCast: noise at %.1f dBFS, %.1f without", got, chat)
	}
}

// editConfig changes the data of a channel's selected config, first
// selecting a copy of a preset, which cannot change.
func (p *pwTest) editConfig(vad string, edit func(data map[string]any)) {
	p.t.Helper()
	var selected []map[string]any
	if err := json.Unmarshal([]byte(p.call("GET", "/configs/selected", "")), &selected); err != nil {
		p.t.Fatal(err)
	}
	for _, c := range selected {
		if c["virtualAudioDevice"] != vad {
			continue
		}
		edit(c["data"].(map[string]any))
		if c["isPreset"] == true {
			delete(c, "id")
			c["name"], c["isPreset"] = "Linux test", false
			var made map[string]any
			json.Unmarshal([]byte(p.call("POST", "/configs", jsonText(c))), &made)
			id, _ := made["id"].(string)
			p.call("PUT", "/configs/"+id+"/select", "")
		} else {
			p.call("PUT", "/configs", jsonText(c))
		}
		return
	}
	p.t.Fatalf("no %s config selected", vad)
}

// setPath sets a value in nested JSON objects by a dotted path.
func setPath(m map[string]any, path string, v any) {
	keys := strings.Split(path, ".")
	for _, k := range keys[:len(keys)-1] {
		next, ok := m[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[k] = next
		}
		m = next
	}
	m[keys[len(keys)-1]] = v
}

// TestDeviceVolumesOnPipeWire reads and sets the volumes of the channels'
// devices on a private PipeWire instance (see newPWTest).
func TestDeviceVolumesOnPipeWire(t *testing.T) {
	p := newPWTest(t)
	volumes := func() map[string]float64 {
		var list []deviceVolume
		if err := json.Unmarshal([]byte(p.call("GET", "/volumeSettings/devices/volumes", "")), &list); err != nil {
			t.Fatal(err)
		}
		out := map[string]float64{}
		for _, v := range list {
			out[v.DeviceID] = v.Volume
		}
		return out
	}
	if v := volumes(); len(v) != 2 || v["test_head"] == 0 {
		t.Fatalf("device volumes: %v, want test_head and test_headset_mic", v)
	}
	if got := p.call("PUT", "/volumeSettings/devices/test_head/volume/0.5", ""); got != "0.5\n" && got != "0.5" {
		t.Errorf("setting the volume returned %q", got)
	}
	if v := volumes()["test_head"]; math.Abs(v-0.5) > 0.01 {
		t.Errorf("test_head at %.3f after setting 0.5", v)
	}
	p.call("PUT", "/volumeSettings/devices/test_head/volume/1", "")
	req, _ := http.NewRequest("PUT", p.srv.URL+"/volumeSettings/devices/elsewhere/volume/1", nil)
	req.Header.Set("Authorization", "Bearer test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a device no channel uses: %d, want 400", resp.StatusCode)
	}
}
