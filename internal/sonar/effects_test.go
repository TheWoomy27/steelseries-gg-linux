package sonar

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestAttenuationLimit(t *testing.T) {
	for mix, want := range map[float64]float64{0: 0, 0.5: 6.02, 0.9: 20, 1: 100} {
		if got := attenuationLimit(mix); math.Abs(got-want) > 0.01 {
			t.Errorf("attenuationLimit(%g) = %.2f, want %.2f", mix, got, want)
		}
	}
}

// withPlugins makes every plugin look installed.
func withPlugins(t *testing.T) {
	orig := pluginPath
	pluginPath = func(p *ladspa) string { return "/usr/lib/ladspa/" + p.lib }
	t.Cleanup(func() { pluginPath = orig })
}

func TestEffectParams(t *testing.T) {
	withPlugins(t)
	mic, _ := channelByRole("ChatCapture")
	data := func(js string) *object {
		var o object
		if err := o.UnmarshalJSON([]byte(js)); err != nil {
			t.Fatal(err)
		}
		return &o
	}
	all := `{"globalEnableState": %t,
		"noiseCancelingState": {"enabled": false, "value": 0.9},
		"noiseReductionState": {"enabled": true, "value": 0.5},
		"impactNoiseReductionState": {"enabled": true, "value": 0.25},
		"noiseGateState": {"enabled": true, "value": -40},
		"automaticNoiseGateState": {"enabled": true},
		"volumeStabilizerState": {"enabled": true, "value": 0.5}}`
	got := effectParams(mic, data(fmt.Sprintf(all, true)), -50)
	want := []any{
		"nc:Attenuation Limit (dB)", 10.0,
		"gate:Bypass", 0.0, "gate:Curve threshold (G)", math.Pow(10, -47.0/20),
		"stab:Bypass", 0.0, "stab:Desired loudness level (LUFS)", -21.0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("effects on:\n got %v\nwant %v", got, want)
	}
	// Without a measured floor the automatic gate keeps the slider's
	// threshold; with Sonar's effects off everything is bypassed.
	if got := effectParams(mic, data(fmt.Sprintf(all, true)), math.NaN()); got[5] != math.Pow(10, -40.0/20) {
		t.Errorf("threshold before the floor is measured: %v", got[5])
	}
	off := effectParams(mic, data(fmt.Sprintf(all, false)), -50)
	if off[1] != 0.0 || off[3] != 1.0 || off[7] != 1.0 {
		t.Errorf("effects off: %v", off)
	}
}

func TestChannelControls(t *testing.T) {
	withPlugins(t)
	chat, _ := channelByRole("ChatRender")
	got := chat.controls([]any{"nc:Attenuation Limit (dB)", 20.0, "eq1:b0", 1.0})
	want := []any{"nc:Attenuation Limit (dB)", 20.0, "eq1_l:b0", 1.0, "eq1_r:b0", 1.0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("chat controls: %v", got)
	}
	mic, _ := channelByRole("ChatCapture")
	if got := mic.controls([]any{"eq1:b0", 1.0}); !reflect.DeepEqual(got, []any{"eq1:b0", 1.0}) {
		t.Errorf("microphone controls: %v", got)
	}
}

func TestSmartVolume(t *testing.T) {
	withPlugins(t)
	game, _ := channelByRole("Game")
	data := func(js string) *object {
		var o object
		if err := o.UnmarshalJSON([]byte(js)); err != nil {
			t.Fatal(err)
		}
		return &o
	}
	got := effectParams(game, data(`{"globalEnableState": true, "smartVolume": {"enabled": true, "volumeLevel": 0.25, "loudness": "extreme"}}`), math.NaN())
	want := []any{"sv:Bypass", 0.0, "sv:Desired loudness level (LUFS)", -16.0, "svmix:Gain 1", 0.25, "svmix:Gain 2", 0.75}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("smart volume on:\n got %v\nwant %v", got, want)
	}
	got = effectParams(game, data(`{"globalEnableState": true, "smartVolume": {"enabled": false, "volumeLevel": 0.25, "loudness": "balanced"}}`), math.NaN())
	want = []any{"sv:Bypass", 1.0, "sv:Desired loudness level (LUFS)", -23.0, "svmix:Gain 1", 0.0, "svmix:Gain 2", 1.0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("smart volume off:\n got %v\nwant %v", got, want)
	}
	// The mix stage takes smart volume's output and the stage before it.
	conf := game.moduleConf(false)
	for _, link := range []string{
		`{ output = "clarity_l:Out" input = "sv:Input L" }`,
		`{ output = "sv:Output R" input = "svmix_r:In 1" }`,
		`{ output = "clarity_r:Out" input = "svmix_r:In 2" }`,
		`{ output = "svmix_l:Out" input = "gain_l:In" }`,
	} {
		if !strings.Contains(conf, link) {
			t.Errorf("Game's graph lacks %s", link)
		}
	}
	if got := game.controls([]any{"sv:Bypass", 1.0, "svmix:Gain 1", 0.5}); !reflect.DeepEqual(got, []any{"sv:Bypass", 1.0, "svmix_l:Gain 1", 0.5, "svmix_r:Gain 1", 0.5}) {
		t.Errorf("smart volume controls: %v", got)
	}
}
