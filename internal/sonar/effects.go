package sonar

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Sonar's voice effects run in its own DSP on Windows. On Linux they are
// LADSPA plugins in the channels' filter graphs:
//
//   - ClearCast noise canceling, on the microphone and on chat, and the
//     microphone's noise reduction are DeepFilterNet. ClearCast's mix
//     factor, the share of the cleaned signal, becomes the plugin's
//     attenuation limit: mixing back the rest of the original holds noise
//     that far down. Noise reduction and impact noise reduction, which
//     Sonar runs only without ClearCast, take up to noiseReductionDB.
//   - The noise gate is LSP's Gate (PipeWire's builtin noisegate never
//     opens in 1.6: its level input defaults to 0 instead of following the
//     signal). The automatic gate keeps its threshold 3 dB over the
//     microphone's noise floor, as Sonar does (see noiseFloor).
//   - The volume stabilizer is LSP's Autogain, its level the loudness it
//     holds the voice at.
//   - Smart volume, on Game, Media and Aux, is LSP's Autogain Stereo,
//     holding the channel at a loudness its loudness type sets. Its level,
//     "the impact of the effect", mixes the leveled signal with the
//     original. Autogain only applies a gain, without latency, so the two
//     stay in phase and the mix is a gain between none and the full
//     correction.
//
// The plugins stay in the graph and are switched by their controls (an
// attenuation limit of 0 turns DeepFilterNet off at no cost, and LSP's
// plugins have a bypass), so changing an effect never restarts the audio.
// An effect whose plugin is not installed is left out.

type ladspa struct {
	lib, label string
	in, out    []string // audio ports, one per audio channel
}

var (
	deepFilterMono    = &ladspa{"libdeep_filter_ladspa.so", "deep_filter_mono", []string{"Audio In"}, []string{"Audio Out"}}
	deepFilterStereo  = &ladspa{"libdeep_filter_ladspa.so", "deep_filter_stereo", []string{"Audio In L", "Audio In R"}, []string{"Audio Out L", "Audio Out R"}}
	lspGate           = &ladspa{"lsp-plugins-ladspa.so", "http://lsp-plug.in/plugins/ladspa/gate_mono", []string{"Input"}, []string{"Output"}}
	lspAutogain       = &ladspa{"lsp-plugins-ladspa.so", "http://lsp-plug.in/plugins/ladspa/autogain_mono", []string{"Input"}, []string{"Output"}}
	lspAutogainStereo = &ladspa{"lsp-plugins-ladspa.so", "http://lsp-plug.in/plugins/ladspa/autogain_stereo", []string{"Input L", "Input R"}, []string{"Output L", "Output R"}}
)

// ladspaDirs are searched after LADSPA_PATH.
var ladspaDirs = []string{
	"/usr/lib/ladspa", "/usr/lib64/ladspa", "/usr/local/lib/ladspa",
	"/usr/lib/x86_64-linux-gnu/ladspa", "/usr/lib/aarch64-linux-gnu/ladspa",
}

var (
	pluginsMu sync.Mutex
	plugins   = map[*ladspa]string{}
)

// pluginPath finds an installed plugin's library, or "". A library that
// lacks the plugin (an older version) would stop the whole graph from
// loading, so the label is looked for in the library too.
var pluginPath = func(p *ladspa) string {
	pluginsMu.Lock()
	defer pluginsMu.Unlock()
	if path, ok := plugins[p]; ok {
		return path
	}
	dirs := append(filepath.SplitList(os.Getenv("LADSPA_PATH")), ladspaDirs...)
	path := ""
	for _, d := range dirs {
		f := filepath.Join(d, p.lib)
		if b, err := os.ReadFile(f); err == nil && bytes.Contains(b, []byte(p.label)) {
			path = f
			break
		}
	}
	plugins[p] = path
	return path
}

// effectPlugins are the plugin stages a channel has, by stage name.
func (c channel) effectPlugins() map[string]*ladspa {
	out := map[string]*ladspa{}
	add := func(stage string, p *ladspa) {
		if pluginPath(p) != "" {
			out[stage] = p
		}
	}
	switch c.role {
	case "ChatCapture":
		add("nc", deepFilterMono)
		add("gate", lspGate)
		add("stab", lspAutogain)
	case "ChatRender":
		add("nc", deepFilterStereo)
	case "Game", "Media", "Aux":
		add("sv", lspAutogainStereo)
	}
	return out
}

// pluginNode is a plugin stage's node, with its controls switched off.
func pluginNode(name string, p *ladspa) string {
	var controls string
	switch p {
	case deepFilterMono, deepFilterStereo:
		controls = `"Attenuation Limit (dB)" = 0`
	case lspGate:
		controls = `"Bypass" = 1 "Reduction (G)" = 0.001 "Attack (ms)" = 5 "Release (ms)" = 150 "Hold time (ms)" = 150 "Sidechain reactivity (ms)" = 10 "Curve zone size (G)" = 0.5 "Pause graph analysis" = 1`
	case lspAutogain:
		controls = `"Bypass" = 1 "Enable maximum amplification gain limitation" = 1 "The maximum amplification gain (dB)" = 24 "The level of silence (LUFS)" = -60`
	case lspAutogainStereo:
		controls = fmt.Sprintf(`"Bypass" = 1 "Enable maximum amplification gain limitation" = 1 "The maximum amplification gain (dB)" = %g "The level of silence (LUFS)" = -60 "Long gain grow amount" = 7 "Long gain fall amount" = 7 "Short gain grow amount" = 7 "Short gain fall amount" = 7`, smartVolumeMaxGainDB)
	}
	return fmt.Sprintf(`{ type = ladspa name = %s plugin = %q label = %q control = { %s } }`, name, pluginPath(p), p.label, controls)
}

const (
	noiseReductionDB = 20.0
	// Sonar's AutomaticNoiseGateOffset.
	autoGateOffsetDB = 3.0
	gateMinDB        = -60.0
	gateMaxDB        = -10.0
	// Smart volume raises quiet audio by at most this much.
	smartVolumeMaxGainDB = 18.0
)

// smartVolumeTargets are the loudness smart volume holds, in LUFS, by
// Sonar's loudness type (the client always leaves it balanced).
var smartVolumeTargets = map[string]float64{"soft": -28, "balanced": -23, "extreme": -16}

// attenuationLimit maps ClearCast's mix factor to DeepFilterNet's
// attenuation limit, in dB (100 is the plugin's unlimited).
func attenuationLimit(mix float64) float64 {
	if mix >= 1 {
		return 100
	}
	if mix <= 0 {
		return 0
	}
	return math.Min(100, -20*math.Log10(1-mix))
}

// effectParams are the controls of a channel's plugin stages. floor is the
// microphone's noise floor in dBFS for the automatic gate (NaN until
// measured).
func effectParams(c channel, data *object, floor float64) []any {
	plugins := c.effectPlugins()
	on := dataBool(data, "globalEnableState")
	var params []any
	if _, ok := plugins["nc"]; ok {
		limit := 0.0
		switch {
		case !on:
		case dataBool(data, "noiseCancelingState", "enabled"):
			limit = attenuationLimit(dataFloat(data, "noiseCancelingState", "value"))
		case c.capture:
			for _, k := range []string{"noiseReductionState", "impactNoiseReductionState"} {
				if dataBool(data, k, "enabled") {
					limit = math.Max(limit, noiseReductionDB*math.Max(0, math.Min(1, dataFloat(data, k, "value"))))
				}
			}
		}
		params = append(params, "nc:Attenuation Limit (dB)", limit)
	}
	if _, ok := plugins["gate"]; ok {
		gateOn := on && dataBool(data, "noiseGateState", "enabled")
		threshold := dataFloat(data, "noiseGateState", "value")
		if autoGate(data) && !math.IsNaN(floor) {
			threshold = floor + autoGateOffsetDB
		}
		threshold = math.Max(gateMinDB, math.Min(gateMaxDB, threshold))
		params = append(params, "gate:Bypass", boolParam(!gateOn), "gate:Curve threshold (G)", math.Pow(10, threshold/20))
	}
	if _, ok := plugins["stab"]; ok {
		stabOn := on && dataBool(data, "volumeStabilizerState", "enabled")
		level := math.Max(0, math.Min(1, dataFloat(data, "volumeStabilizerState", "value")))
		params = append(params, "stab:Bypass", boolParam(!stabOn), "stab:Desired loudness level (LUFS)", -30+18*level)
	}
	if _, ok := plugins["sv"]; ok {
		svOn := on && dataBool(data, "smartVolume", "enabled")
		target, ok := smartVolumeTargets[strings.ToLower(fmt.Sprint(dataPath(data, "smartVolume", "loudness")))]
		if !ok {
			target = smartVolumeTargets["balanced"]
		}
		wet := 0.0
		if svOn {
			wet = math.Max(0, math.Min(1, dataFloat(data, "smartVolume", "volumeLevel")))
		}
		params = append(params, "sv:Bypass", boolParam(!svOn), "sv:Desired loudness level (LUFS)", target, "svmix:Gain 1", wet, "svmix:Gain 2", 1-wet)
	}
	return params
}

// autoGate reports whether a microphone config gates at its noise floor.
func autoGate(data *object) bool {
	return dataBool(data, "globalEnableState") && dataBool(data, "noiseGateState", "enabled") && dataBool(data, "automaticNoiseGateState", "enabled")
}

func boolParam(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
