package sonar

import (
	"math"
	"math/rand"
	"path/filepath"
	"testing"
	"time"
)

// TestSurroundOnPipeWire plays 7.1 audio into Game on a private PipeWire
// instance (see newPWTest) and measures each ear of the headset, with
// surround off (the downmix) and on.
func TestSurroundOnPipeWire(t *testing.T) {
	if hrtfPath() == "" {
		t.Skip("PipeWire's SOFA plugin or an HRTF set is not installed")
	}
	p := newPWTest(t)
	objs, err := pwDump()
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if name, _ := o.Info.Props["node.name"].(string); name == "sonar_game" && propString(o.Info.Props, "audio.channels") != "8" {
			t.Errorf("Game's device has %s channels, want 8", propString(o.Info.Props, "audio.channels"))
		}
	}

	// speakers writes noise at -30 dBFS RMS into the named speakers, each
	// its own, from start to end (fractions of the file).
	const d = 3 * time.Second
	n := int(d.Seconds() * sampleRate)
	noise := func(name string, start, end float64, pos ...string) string {
		path := filepath.Join(p.tmp, name+".wav")
		r := rand.New(rand.NewSource(1))
		chans := make([][]float64, len(speakers))
		for i, sp := range speakers {
			chans[i] = make([]float64, n)
			for _, want := range pos {
				if sp.pos == want {
					for k := int(start * float64(n)); k < int(end*float64(n)); k++ {
						chans[i][k] = 0.0316 * r.NormFloat64()
					}
				}
			}
		}
		writeChannels(t, path, chans)
		return path
	}
	ears := func(wav string) (l, r float64) {
		t.Helper()
		chans := readWAV(t, p.record("test_head", "sonar_game", wav))
		mid := func(x []float64) []float64 { return x[len(x)/4 : len(x)*3/4] }
		return levelDB(mid(chans[0])), levelDB(mid(chans[1]))
	}
	set := func(kv ...any) {
		t.Helper()
		p.editConfig("game", func(data map[string]any) {
			for i := 0; i < len(kv); i += 2 {
				setPath(data, kv[i].(string), kv[i+1])
			}
		})
	}
	front, center, side := noise("front", 0, 1, "FL", "FR"), noise("center", 0, 1, "FC"), noise("side", 0, 1, "SL")
	frontLeft := noise("frontLeft", 0, 1, "FL")

	set("globalEnableState", true, "parametricEQ.enabled", false, "bassBoostState.enabled", false, "trebleBoostState.enabled", false,
		"voiceClarityState.enabled", false, "generalGain", 0.0, "virtualSurroundState", false, "reverbGainDB", -12.0)
	l, r := ears(front)
	t.Logf("downmix, front pair: %.1f %.1f dBFS", l, r)
	plain := l
	l, r = ears(center)
	p.near("downmix, center on the left", l, plain-3)
	p.near("downmix, center on the right", r, plain-3)
	l, r = ears(side)
	p.near("downmix, side left on the left", l, plain-3)
	p.silent("downmix, side left on the right", r)

	set("virtualSurroundState", true)
	l, r = ears(front)
	t.Logf("surround, front pair: %.1f %.1f dBFS", l, r)
	if math.Abs(l-plain) > 3 || math.Abs(r-plain) > 3 {
		t.Errorf("surround changes the front pair's loudness: %.1f %.1f dBFS, %.1f without", l, r, plain)
	}
	fl, fr := ears(frontLeft)
	t.Logf("surround, front left: %.1f %.1f dBFS", fl, fr)
	if fl-fr < 3 || fr < -70 {
		t.Errorf("front left should reach both ears, the left louder: %.1f %.1f dBFS", fl, fr)
	}
	sl, sr := ears(side)
	t.Logf("surround, side left: %.1f %.1f dBFS", sl, sr)
	if sl-sr < fl-fr+3 {
		t.Errorf("side left should be further left than front left: %.1f %.1f dBFS", sl, sr)
	}

	// Moving front left to the right and bringing it closer.
	set("virtualSurroundChannels.frontLeft.position", -30.0)
	l, r = ears(frontLeft)
	t.Logf("front left moved to -30: %.1f %.1f dBFS", l, r)
	if r-l < 3 {
		t.Errorf("front left at -30 should be louder on the right: %.1f %.1f dBFS", l, r)
	}
	set("virtualSurroundChannels.frontLeft.position", 30.0, "virtualSurroundChannels.frontLeft.gain", 12.0)
	l, _ = ears(frontLeft)
	if l-fl < 10 || l-fl > 13 {
		t.Errorf("front left at 12 dB: %.1f dBFS, want 12 dB over %.1f", l, fl)
	} else {
		t.Logf("front left at 12 dB: %.1f dBFS", l)
	}
	set("virtualSurroundChannels.frontLeft.gain", 0.0)

	// Speaker mode cancels the crosstalk speakers add, so the far ear's
	// share falls further.
	set("formFactor", "speakers")
	l, r = ears(front)
	t.Logf("speaker mode, front pair: %.1f %.1f dBFS", l, r)
	if math.Abs(l-plain) > 3 || math.Abs(r-plain) > 3 {
		t.Errorf("speaker mode changes the front pair's loudness: %.1f %.1f dBFS, %.1f without", l, r, plain)
	}
	l, r = ears(frontLeft)
	t.Logf("speaker mode, front left: %.1f %.1f dBFS", l, r)
	if l-r < fl-fr+2 {
		t.Errorf("speaker mode should separate front left further than headphones (%.1f dB): %.1f %.1f dBFS", fl-fr, l, r)
	}
	set("formFactor", "headphones")

	// The room: noise stops halfway through, and the reverb's tail follows.
	burst := noise("burst", 0.1, 0.5, "FL", "FR")
	tail := func() float64 {
		t.Helper()
		left := readWAV(t, p.record("test_head", "sonar_game", burst))[0]
		start := -1
		for i, v := range left {
			if math.Abs(v) > 0.01 {
				start = i
				break
			}
		}
		end := start + int(0.4*float64(n))
		if start < 0 || end+sampleRate/4 > len(left) {
			t.Fatalf("burst not found in the recording")
		}
		// From 50 to 250 ms after the burst, against the burst.
		return levelDB(left[end+sampleRate/20:end+sampleRate/4]) - levelDB(left[start+sampleRate/10:end-sampleRate/10])
	}
	set("reverbGainDB", 0.0)
	immersion := tail()
	set("reverbGainDB", -12.0)
	performance := tail()
	t.Logf("reverb tail: %.1f dB at Immersion, %.1f dB at Performance", immersion, performance)
	if math.Abs(immersion-performance-12) > 2 {
		t.Errorf("the reverb's tail should drop by 12 dB from Immersion to Performance")
	}
	set("virtualSurroundState", false)
	if off := tail(); off > -60 {
		t.Errorf("reverb tail with surround off: %.1f dB", off)
	}
}

// TestSmartVolumeOnPipeWire plays loud and quiet noise into Game with smart
// volume on a private PipeWire instance (see newPWTest).
func TestSmartVolumeOnPipeWire(t *testing.T) {
	if pluginPath(lspAutogainStereo) == "" {
		t.Skip("LSP's Autogain Stereo is not installed")
	}
	p := newPWTest(t)
	const d = 5 * time.Second
	loud, quiet := filepath.Join(p.tmp, "loud.wav"), filepath.Join(p.tmp, "quiet.wav")
	writeNoise(t, loud, d, -12, false)
	writeNoise(t, quiet, d, -45, false)
	// The first play lets the gain settle.
	game := func(wav string) float64 {
		t.Helper()
		p.level("test_head", "sonar_game", wav)
		return p.level("test_head", "sonar_game", wav)
	}
	set := func(kv ...any) {
		t.Helper()
		p.editConfig("game", func(data map[string]any) {
			for i := 0; i < len(kv); i += 2 {
				setPath(data, kv[i].(string), kv[i+1])
			}
		})
	}
	set("globalEnableState", true, "parametricEQ.enabled", false, "bassBoostState.enabled", false, "trebleBoostState.enabled", false,
		"voiceClarityState.enabled", false, "generalGain", 0.0, "virtualSurroundState", false,
		"smartVolume.enabled", false, "smartVolume.volumeLevel", 1.0, "smartVolume.loudness", "balanced")
	loudOff, quietOff := p.level("test_head", "sonar_game", loud), p.level("test_head", "sonar_game", quiet)
	t.Logf("off: loud %.1f, quiet %.1f dBFS", loudOff, quietOff)

	set("smartVolume.enabled", true)
	loudOn, quietOn := game(loud), game(quiet)
	t.Logf("on: loud %.1f, quiet %.1f dBFS", loudOn, quietOn)
	if loudOn > loudOff-10 || quietOn < quietOff+10 || math.Abs(loudOn-quietOn) > 6 {
		t.Errorf("smart volume should bring loud and quiet noise together")
	}

	// At half the impact the original and the leveled noise add in phase.
	set("smartVolume.volumeLevel", 0.5)
	g := math.Pow(10, (quietOn-quietOff)/20)
	p.near("quiet noise at half the impact", game(quiet), quietOff+20*math.Log10(0.5*g+0.5))

	set("smartVolume.enabled", false)
	p.near("loud noise with smart volume off again", p.level("test_head", "sonar_game", loud), loudOff)
}
