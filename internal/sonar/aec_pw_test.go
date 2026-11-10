package sonar

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// TestEchoCancelOnPipeWire plays noise on the speakers (test_head, through
// the Gaming channel) and lets the test microphone hear them, as speakers
// next to a microphone are heard: with echo canceling on, the microphone
// channel carries much less of it, and the microphone itself still goes
// through.
func TestEchoCancelOnPipeWire(t *testing.T) {
	p := newPWTest(t)
	var feature struct {
		ID        string `json:"id"`
		IsEnabled bool   `json:"isEnabled"`
	}
	json.Unmarshal([]byte(p.call("GET", "/features/SonarCiaran", "")), &feature)
	if !feature.IsEnabled {
		t.Fatalf("echo canceling is not offered with test_head (a null sink, speakers): %+v", feature)
	}
	nodes, _, _, _, err := readGraph()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{aecSource, aecCapture, aecReference} {
		if _, ok := nodes[n]; !ok {
			t.Fatalf("no %s in the graph", n)
		}
	}

	// The acoustic path from the speakers to the microphone.
	for _, ch := range []string{"FL", "FR"} {
		if out, err := pwCommand("pw-link", "test_head:monitor_"+ch, "test_headset_mic:input_MONO").CombinedOutput(); err != nil {
			t.Fatalf("pw-link: %v: %s", err, out)
		}
	}
	p.editConfig("chatCapture", func(data map[string]any) {
		for k, v := range map[string]any{"globalEnableState": true, "parametricEQ.enabled": false, "noiseCancelingState.enabled": false,
			"noiseReductionState.enabled": false, "impactNoiseReductionState.enabled": false, "noiseGateState.enabled": false,
			"automaticNoiseGateState.enabled": false, "volumeStabilizerState.enabled": false, "acousticEchoCancelingState": false} {
			setPath(data, k, v)
		}
	})
	noise := filepath.Join(p.tmp, "noise.wav")
	writeNoise(t, noise, 6*time.Second, -20, false)
	tone := filepath.Join(p.tmp, "tone.wav")
	writeTone(t, tone, 3*time.Second)
	// The canceller adapts during the first seconds; the end tells.
	tail := func(rec string) float64 {
		x := readWAV(t, rec)
		var mono []float64
		for _, ch := range x {
			if len(mono) == 0 {
				mono = make([]float64, len(ch))
			}
			for i, v := range ch {
				mono[i] += v / float64(len(x))
			}
		}
		return levelDB(mono[len(mono)*6/10:])
	}

	echo := tail(p.record("sonar_mic", "sonar_game", noise))
	plain := p.level("sonar_mic", "test_headset_mic", tone)
	t.Logf("echo without canceling: %.1f dBFS; microphone: %.1f dBFS", echo, plain)
	if echo < -40 {
		t.Fatalf("the microphone does not hear the speakers (%.1f dBFS)", echo)
	}

	p.editConfig("chatCapture", func(data map[string]any) { data["acousticEchoCancelingState"] = true })
	time.Sleep(time.Second)
	nodes, _, ports, links, _ := readGraph()
	linked := func(from, to string) bool {
		for _, l := range links {
			var out, in int
			for _, pt := range ports {
				if pt.id == l.out {
					out = pt.node
				}
				if pt.id == l.in {
					in = pt.node
				}
			}
			if out == nodes[from] && in == nodes[to] {
				return true
			}
		}
		return false
	}
	for _, l := range [][2]string{{"test_headset_mic", aecCapture}, {aecSource, "sonar_mic.device"}, {"test_head", aecReference}} {
		if !linked(l[0], l[1]) {
			t.Errorf("%s is not linked to %s", l[0], l[1])
		}
	}
	if linked("test_headset_mic", "sonar_mic.device") {
		t.Error("the microphone still goes straight to the microphone channel")
	}
	canceled := tail(p.record("sonar_mic", "sonar_game", noise))
	t.Logf("echo with canceling: %.1f dBFS", canceled)
	if canceled > echo-15 {
		t.Errorf("echo canceling: %.1f dBFS, want 15 dB under %.1f", canceled, echo)
	}
	// Without sound from the speakers, the microphone goes through.
	if got := p.level("sonar_mic", "test_headset_mic", tone); got < plain-6 {
		t.Errorf("the microphone through the canceller: %.1f dBFS, want about %.1f", got, plain)
	} else {
		t.Logf("microphone through the canceller: %.1f dBFS", got)
	}

	p.editConfig("chatCapture", func(data map[string]any) { data["acousticEchoCancelingState"] = false })
	time.Sleep(time.Second)
	nodes, _, ports, links, _ = readGraph()
	if !linked("test_headset_mic", "sonar_mic.device") || linked(aecSource, "sonar_mic.device") {
		t.Error("turning echo canceling off did not link the microphone straight again")
	}
}
