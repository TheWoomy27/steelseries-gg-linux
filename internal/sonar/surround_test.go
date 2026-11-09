package sonar

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// withHRTF makes virtual surround look available.
func withHRTF(t *testing.T) {
	orig := hrtfPath
	hrtfPath = func() string { return "/usr/share/libmysofa/default.sofa" }
	t.Cleanup(func() { hrtfPath = orig })
}

func TestAzimuth(t *testing.T) {
	for pos, want := range map[float64]float64{30: 30, -30: 330, -150: 210, 150: 150, 0: 0, -180: 180, 360: 0} {
		if got := azimuth(pos); got != want {
			t.Errorf("azimuth(%g) = %g, want %g", pos, got, want)
		}
	}
}

func TestSurroundParams(t *testing.T) {
	withHRTF(t)
	game, _ := channelByRole("Game")
	data := func(js string) *object {
		var o object
		if err := o.UnmarshalJSON([]byte(js)); err != nil {
			t.Fatal(err)
		}
		return &o
	}
	on := data(`{"globalEnableState": true, "virtualSurroundState": true, "reverbGainDB": -6,
		"virtualSurroundChannels": {"frontLeft": {"position": 45, "gain": 6}, "rearRight": {"position": -150, "gain": 20}}}`)
	got := map[string]any{}
	params := surroundParams(game, on)
	for i := 0; i < len(params); i += 2 {
		got[params[i].(string)] = params[i+1]
	}
	db := func(v float64) float64 { return math.Pow(10, v/20) }
	for k, want := range map[string]float64{
		"sur_l:Gain 1": 0, "sur_l:Gain 2": 1, "sur_r:Gain 3": db(-6 + reverbOffsetDB),
		"vs_FL:Azimuth": 45, "vs_FR:Azimuth": 330, "vs_RR:Azimuth": 210,
		"hrtf_l:Gain 1": db(hrtfLevelDB + 6), "hrtf_r:Gain 2": db(hrtfLevelDB),
		"hrtf_l:Gain 4": db(hrtfLevelDB) * lfeLevel, "hrtf_r:Gain 6": db(hrtfLevelDB + speakerGainMax),
	} {
		if v, _ := got[k].(float64); math.Abs(v-want) > 1e-9 {
			t.Errorf("%s = %v, want %g", k, got[k], want)
		}
	}
	spk := data(`{"globalEnableState": true, "virtualSurroundState": true, "formFactor": "speakers", "reverbGainDB": -12}`)
	if p := surroundParams(game, spk); !reflect.DeepEqual(p[:8], []any{"sur_l:Gain 1", 0.0, "sur_l:Gain 2", 0.0, "sur_l:Gain 3", db(reverbGainMin + reverbOffsetDB), "sur_l:Gain 4", db(xtcLevelDB)}) {
		t.Errorf("speaker mode: %v", p[:8])
	}
	// Off, the downmix plays alone; Sonar's effects off turn it off too.
	for _, js := range []string{`{"globalEnableState": true, "virtualSurroundState": false}`, `{"globalEnableState": false, "virtualSurroundState": true}`} {
		off := surroundParams(game, data(js))
		if !reflect.DeepEqual(off[:8], []any{"sur_l:Gain 1", 1.0, "sur_l:Gain 2", 0.0, "sur_l:Gain 3", 0.0, "sur_l:Gain 4", 0.0}) {
			t.Errorf("%s: %v", js, off[:8])
		}
	}
	chat, _ := channelByRole("ChatRender")
	if p := surroundParams(chat, on); p != nil {
		t.Errorf("chat has no surround: %v", p)
	}
}

func TestSurroundGraph(t *testing.T) {
	withHRTF(t)
	game, _ := channelByRole("Game")
	conf := game.moduleConf(false)
	for _, want := range []string{
		"audio.channels = 8 audio.position = [ FL FR FC LFE RL RR SL SR ]",
		`inputs = [ "in_FL:In" "in_FR:In" "in_FC:In" "in_LFE:In" "in_RL:In" "in_RR:In" "in_SL:In" "in_SR:In" ]`,
		`{ output = "sur_l:Out" input = "eq1_l:In" }`,
		`{ output = "vs_SL:Out L" input = "hrtf_l:In 7" }`,
		`{ type = builtin name = dmx_r label = mixer control = { "Gain 2" = 1 "Gain 3" = 0.7071067811865476 "Gain 4" = 0.3535533905932738 "Gain 6" = 0.7071067811865476 "Gain 8" = 0.7071067811865476 } }`,
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("Game's graph lacks %s", want)
		}
	}
	hrtfPath = func() string { return "" }
	if conf := game.moduleConf(false); strings.Contains(conf, "sofa") || !strings.Contains(conf, `{ output = "dmx_l:Out" input = "eq1_l:In" }`) {
		t.Errorf("without an HRTF set Game should only downmix:\n%s", conf)
	}
}

func TestSurroundControls(t *testing.T) {
	game, _ := channelByRole("Game")
	got := game.controls([]any{"eq1:b0", 1.0, "mon:Mult", 0.5, "sur_l:Gain 1", 0.0, "vs_FL:Azimuth", 30.0})
	want := []any{"eq1_l:b0", 1.0, "eq1_r:b0", 1.0, "mon_l:Mult", 0.5, "mon_r:Mult", 0.5, "sur_l:Gain 1", 0.0, "vs_FL:Azimuth", 30.0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Game's controls:\n got %v\nwant %v", got, want)
	}
}

func TestRoomIR(t *testing.T) {
	ir := roomIR()
	var cross float64
	for ch, x := range ir {
		if len(x) != int(roomRT60*roomRate) {
			t.Fatalf("channel %d: %d samples", ch, len(x))
		}
		var all, early, late float64
		for i, v := range x {
			e := float64(v) * float64(v)
			all += e
			switch {
			case i < roomRate/10:
				early += e
			case i >= len(x)-roomRate/10:
				late += e
			}
			cross += float64(ir[0][i]) * float64(ir[1][i]) / 2
		}
		if math.Abs(all-1) > 1e-4 {
			t.Errorf("channel %d: energy %g, want 1", ch, all)
		}
		if late > early/1000 {
			t.Errorf("channel %d does not decay: %g in the last 100 ms, %g in the first", ch, late, early)
		}
	}
	if math.Abs(cross) > 0.1 {
		t.Errorf("the channels are correlated: %.2f", cross)
	}
}

// TestCrosstalkCanceller plays the canceller's output through the
// crosstalk it models: the ears should hear the input alone.
func TestCrosstalkCanceller(t *testing.T) {
	direct, cross := xtcIR()
	toR, toL := xtcCrosstalk(), xtcCrosstalk()
	var leak, err float64
	for n := range direct {
		var fromL, fromR float64
		if n >= xtcDelay {
			fromL, fromR = toR(float64(direct[n-xtcDelay])), toL(float64(cross[n-xtcDelay]))
		} else {
			toR(0)
			toL(0)
		}
		want := 0.0
		if n == 0 {
			want = 1
		}
		left, right := float64(direct[n])+fromR, float64(cross[n])+fromL
		err += (left - want) * (left - want)
		leak += right * right
	}
	if err > 1e-6 || leak > 1e-6 {
		t.Errorf("the ears hear %g besides the input and %g from the other side", err, leak)
	}
	var crossEnergy float64
	for _, v := range cross {
		crossEnergy += float64(v) * float64(v)
	}
	if crossEnergy < 0.1 {
		t.Errorf("the canceller hardly crosses over: %g", crossEnergy)
	}
}
