package sonar

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Virtual audio devices (Sonar's channels), as stored in configs.vad.
const (
	vadGame        = 1
	vadChatRender  = 2
	vadChatCapture = 3
	vadMedia       = 4
	vadAux         = 5
)

var vadNames = map[int]string{
	vadGame: "Game", vadChatRender: "ChatRender", vadChatCapture: "ChatCapture", vadMedia: "Media", vadAux: "Aux",
}

func vadByName(name string) (int, bool) {
	for v, n := range vadNames {
		if n == name {
			return v, true
		}
	}
	return 0, false
}

// latestSchema is each channel's current config data schema.
var latestSchema = map[int]int{vadGame: 5, vadChatRender: 5, vadChatCapture: 6, vadMedia: 5, vadAux: 5}

const presetImage = "sonar.svg"

var vadImages = map[int]string{
	vadGame: "8.svg", vadChatRender: "30.svg", vadChatCapture: "23.svg", vadMedia: "14.svg", vadAux: "15.svg",
}

func defaultImage(vad int, preset bool) string {
	if preset {
		return presetImage
	}
	return vadImages[vad]
}

// Data layouts: playback channels (Game, Media, Aux) and capture-style
// channels (Chat and Microphone), in Sonar's property order.
var renderKeys = []string{
	"bassBoostState", "trebleBoostState", "voiceClarityState", "smartVolume", "generalGain", "parametricEQ",
	"virtualSurroundState", "virtualSurroundChannels", "reverbGainDB", "formFactor", "globalEnableState",
}

var captureKeys = []string{
	"noiseReductionState", "volumeStabilizerState", "noiseGateState", "automaticNoiseGateState", "parametricEQ",
	"impactNoiseReductionState", "noiseCancelingState", "acousticEchoCancelingState", "globalEnableState",
}

func eqFilter(enabled bool, freq float64, typ string, gain float64) *object {
	o := newObject()
	o.set("enabled", enabled)
	o.set("qFactor", num(0.7071))
	o.set("frequency", num(freq))
	o.set("gain", num(gain))
	o.set("type", typ)
	return o
}

func defaultParametricEQ() *object {
	o := newObject()
	o.set("enabled", true)
	for i, f := range []float64{35, 120, 1000, 6000, 18000, 1000, 2000, 4000, 8000, 16000} {
		o.set(fmt.Sprintf("filter%d", i+1), eqFilter(i < 5, f, "peakingEQ", 0))
	}
	return o
}

func defaultSurroundChannels() *object {
	o := newObject()
	for _, ch := range []struct {
		name string
		pos  float64
	}{{"frontLeft", 30}, {"frontRight", -30}, {"center", 0}, {"subWoofer", 0}, {"rearLeft", 150}, {"rearRight", -150}, {"sideLeft", 90}, {"sideRight", -90}} {
		c := newObject()
		c.set("position", num(ch.pos))
		c.set("gain", num(0))
		o.set(ch.name, c)
	}
	return o
}

// defaultData is the data of a new config without any, as Sonar's
// GenericConfigToVadConfigConverter builds it.
func defaultData(vad int) *object {
	o := newObject()
	if vad == vadChatRender || vad == vadChatCapture {
		o.set("noiseReductionState", state(false, 0))
		o.set("volumeStabilizerState", state(false, 0))
		o.set("noiseGateState", state(false, -60))
		o.set("automaticNoiseGateState", state(false, 3))
		o.set("parametricEQ", defaultParametricEQ())
		o.set("impactNoiseReductionState", state(false, 0))
		o.set("noiseCancelingState", state(vad == vadChatCapture, 0.9))
		o.set("acousticEchoCancelingState", false)
		o.set("globalEnableState", true)
		return o
	}
	o.set("bassBoostState", state(false, 0))
	o.set("trebleBoostState", state(false, 0))
	o.set("voiceClarityState", state(false, 0))
	sv := newObject()
	sv.set("enabled", false)
	sv.set("volumeLevel", num(0))
	sv.set("loudness", "balanced")
	o.set("smartVolume", sv)
	o.set("generalGain", num(0))
	o.set("parametricEQ", defaultParametricEQ())
	o.set("virtualSurroundState", false)
	o.set("virtualSurroundChannels", defaultSurroundChannels())
	o.set("reverbGainDB", num(-6))
	o.set("formFactor", "headphones")
	o.set("globalEnableState", true)
	return o
}

// withKeys lays data out in a schema's property order, filling missing
// properties from defaults and dropping unknown ones (Sonar's upgraders
// re-serialize the data through typed models).
func withKeys(data *object, keys []string, defaults *object) *object {
	o := newObject()
	for _, k := range keys {
		if data.has(k) {
			o.set(k, data.get(k))
		} else {
			o.set(k, defaults.get(k))
		}
	}
	return o
}

// The 10-band graphic equalizer of schema 1 capture configs became a
// parametric EQ with these bands.
var graphicBands = []struct {
	key  string
	freq float64
	typ  string
}{
	{"31Hz", 31, "lowShelving"}, {"62Hz", 62, "peakingEQ"}, {"125Hz", 125, "peakingEQ"}, {"250Hz", 250, "peakingEQ"},
	{"500Hz", 500, "peakingEQ"}, {"1000Hz", 1000, "peakingEQ"}, {"2000Hz", 2000, "peakingEQ"},
	{"4000Hz", 4000, "peakingEQ"}, {"8000Hz", 8000, "peakingEQ"}, {"16000Hz", 16000, "highShelving"},
}

func parametricFromGraphic(eq *object) *object {
	o := newObject()
	enabled, ok := eq.get("enabled").(bool)
	o.set("enabled", enabled || !ok)
	for i, b := range graphicBands {
		o.set(fmt.Sprintf("filter%d", i+1), eqFilter(true, b.freq, b.typ, toFloat(eq.get(b.key))))
	}
	return o
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

// upgrade brings a config to its channel's latest schema, as Sonar's
// per-channel upgraders do (SoundStage.Services.Configs.Upgrader.VadConfig).
func upgrade(c *config) error {
	latest, ok := latestSchema[c.VAD]
	if !ok {
		return fmt.Errorf("unknown virtual audio device %d", c.VAD)
	}
	if c.SchemaVersion >= latest {
		return nil
	}
	if c.SchemaVersion < 1 {
		return fmt.Errorf("config schema version %d out of range", c.SchemaVersion)
	}
	data, ok := c.Data.(*object)
	if !ok {
		return fmt.Errorf("unexpected data format")
	}
	defaults := defaultData(c.VAD)
	switch c.VAD {
	case vadGame, vadMedia, vadAux:
		c.Data = withKeys(data, renderKeys, defaults)
	case vadChatRender, vadChatCapture:
		if eq := data.child("equalizer"); eq != nil && !data.has("parametricEQ") {
			data.set("parametricEQ", parametricFromGraphic(eq))
		}
		c.Data = withKeys(data, captureKeys, defaults)
		if c.SchemaVersion == 1 && c.VAD == vadChatCapture {
			if dd, ok := c.DefaultData.(*object); ok {
				if nc := dd.child("noiseCancelingState"); nc != nil {
					nc.set("enabled", true)
				}
			}
		}
	}
	c.SchemaVersion = latest
	c.Image = defaultImage(c.VAD, c.IsPreset)
	return nil
}
