package sonar

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"ggl/internal/paths"
)

// Sonar's virtual surround, on the Game, Media and Aux channels, renders
// 7.1 audio for headphones, with a reverb ("Immersion") for the room. On
// Linux:
//
//   - The channel's virtual device is 7.1, as Sonar's are on Windows.
//     Stereo apps play to its front pair (PipeWire does not upmix them).
//   - Each speaker goes through PipeWire's SOFA spatializer: a head-related
//     transfer function at the speaker's angle. Sonar's angles turn
//     counterclockwise from the front, as SOFA's azimuths do. The HRTF set
//     is hrtf.sofa in GG's sonar data folder if there is one, otherwise
//     libmysofa's MIT KEMAR.
//   - The reverb convolves the plain downmix with a small room's impulse
//     response that ggd generates.
//   - Speaker mode (the form factor) plays the virtual speakers over a
//     pair of speakers: a crosstalk canceller keeps each ear's signal from
//     reaching the other ear. It is Glasgal's recursive ambiophonic
//     crosstalk elimination (RACE), unrolled into FIR filters, since a
//     filter graph cannot loop.
//   - With surround off the speakers are downmixed with PipeWire's own
//     coefficients, so the channel sounds as it did with a stereo device.
//
// As with the effects, everything stays in the graph: mixer gains switch
// between the downmix and the virtual speakers, and the spatializers'
// angles are controls, so turning surround on or moving a speaker never
// restarts the audio. A channel's virtualizer costs about 1% of a core.
// Without the SOFA plugin or an HRTF set the device is still 7.1 and is
// downmixed.

type speaker struct {
	key         string  // in virtualSurroundChannels
	pos         string  // PipeWire's channel position
	angle       float64 // Sonar's default position
	left, right float64 // the downmix
}

// PipeWire's downmix levels for the center, LFE and surround channels.
const (
	centerLevel   = math.Sqrt2 / 2
	lfeLevel      = math.Sqrt2 / 4
	surroundLevel = math.Sqrt2 / 2
)

var speakers = []speaker{
	{"frontLeft", "FL", 30, 1, 0},
	{"frontRight", "FR", -30, 0, 1},
	{"center", "FC", 0, centerLevel, centerLevel},
	{"subWoofer", "LFE", 0, lfeLevel, lfeLevel},
	{"rearLeft", "RL", 150, surroundLevel, 0},
	{"rearRight", "RR", -150, 0, surroundLevel},
	{"sideLeft", "SL", 90, surroundLevel, 0},
	{"sideRight", "SR", -90, 0, surroundLevel},
}

const surroundPosition = "[ FL FR FC LFE RL RR SL SR ]"

const (
	// hrtfLevelDB matches the virtual speakers' loudness to the downmix's
	// (measured with MIT KEMAR, for noise in the front pair).
	hrtfLevelDB = -4.3
	// reverbOffsetDB places Sonar's reverb gain (-12 to 0 dB) against the
	// direct sound: the room's response has the energy of its input.
	reverbOffsetDB = -9.0
	speakerGainMax = 12.0
	reverbGainMin  = -12.0
	// xtcLevelDB matches speaker mode's loudness to the downmix's.
	xtcLevelDB = 0.0
)

var spaDirs = []string{
	"/usr/lib/spa-0.2", "/usr/lib64/spa-0.2", "/usr/local/lib/spa-0.2",
	"/usr/lib/x86_64-linux-gnu/spa-0.2", "/usr/lib/aarch64-linux-gnu/spa-0.2",
}

// hrtfPath is the HRTF set for virtual surround, or "" if PipeWire's SOFA
// plugin or an HRTF set is missing (the graph would not load).
var hrtfPath = sync.OnceValue(func() string {
	plugin := false
	for _, d := range append(filepath.SplitList(os.Getenv("SPA_PLUGIN_DIR")), spaDirs...) {
		if _, err := os.Stat(filepath.Join(d, "filter-graph", "libspa-filter-graph-plugin-sofa.so")); err == nil {
			plugin = true
			break
		}
	}
	if !plugin {
		return ""
	}
	for _, f := range []string{
		filepath.Join(paths.Data(), "sonar", "hrtf.sofa"),
		"/usr/share/libmysofa/default.sofa", "/usr/local/share/libmysofa/default.sofa",
	} {
		if _, err := os.Stat(f); err == nil {
			return f
		}
	}
	return ""
})

// surroundGraph is the head of a surround channel's graph: the 7.1 inputs,
// downmixed or rendered for headphones, ending at outs (left, right).
func surroundGraph() (nodes, links, inputs []string, outs [2]string) {
	hrtf := hrtfPath()
	sides := [2]string{"l", "r"}
	for i, sp := range speakers {
		in := "in_" + sp.pos
		nodes = append(nodes, fmt.Sprintf(`{ type = builtin name = %s label = copy }`, in))
		inputs = append(inputs, strconv.Quote(in+":In"))
		for s, level := range [2]float64{sp.left, sp.right} {
			if level != 0 {
				links = append(links, fmt.Sprintf(`{ output = "%s:Out" input = "dmx_%s:In %d" }`, in, sides[s], i+1))
			}
		}
		if hrtf != "" {
			vs := "vs_" + sp.pos
			nodes = append(nodes, fmt.Sprintf(`{ type = sofa name = %s label = spatializer config = { filename = %q } control = { "Azimuth" = %g "Elevation" = 0 } }`, vs, hrtf, azimuth(sp.angle)))
			links = append(links, fmt.Sprintf(`{ output = "%s:Out" input = "%s:In" }`, in, vs),
				fmt.Sprintf(`{ output = "%s:Out L" input = "hrtf_l:In %d" }`, vs, i+1),
				fmt.Sprintf(`{ output = "%s:Out R" input = "hrtf_r:In %d" }`, vs, i+1))
		}
	}
	for s, side := range sides {
		var gains []string
		for i, sp := range speakers {
			if level := [2]float64{sp.left, sp.right}[s]; level != 0 {
				gains = append(gains, fmt.Sprintf(`"Gain %d" = %g`, i+1, level))
			}
		}
		nodes = append(nodes, fmt.Sprintf(`{ type = builtin name = dmx_%s label = mixer control = { %s } }`, side, strings.Join(gains, " ")))
		outs[s] = "dmx_" + side + ":Out"
		if hrtf == "" {
			continue
		}
		// The virtual speakers and the room join the downmix at the
		// surround mixer, which starts with surround off.
		nodes = append(nodes,
			fmt.Sprintf(`{ type = builtin name = hrtf_%s label = mixer }`, side),
			fmt.Sprintf(`{ type = builtin name = rev_%s label = convolver config = { filename = %q channel = %d } }`, side, roomPath(), s),
			fmt.Sprintf(`{ type = builtin name = sur_%s label = mixer control = { "Gain 1" = 1 "Gain 2" = 0 "Gain 3" = 0 "Gain 4" = 0 } }`, side))
		links = append(links,
			fmt.Sprintf(`{ output = "dmx_%s:Out" input = "sur_%s:In 1" }`, side, side),
			fmt.Sprintf(`{ output = "hrtf_%s:Out" input = "sur_%s:In 2" }`, side, side),
			fmt.Sprintf(`{ output = "dmx_%s:Out" input = "rev_%s:In" }`, side, side),
			fmt.Sprintf(`{ output = "rev_%s:Out" input = "sur_%s:In 3" }`, side, side))
		// Speaker mode: each side's binaural signal, directly and crossed
		// to the other side.
		other := sides[1-s]
		nodes = append(nodes,
			fmt.Sprintf(`{ type = builtin name = xtc_%s%s label = convolver config = { filename = %q channel = 0 } }`, side, side, xtcPath()),
			fmt.Sprintf(`{ type = builtin name = xtc_%s%s label = convolver config = { filename = %q channel = 1 } }`, side, other, xtcPath()),
			fmt.Sprintf(`{ type = builtin name = spk_%s label = mixer }`, side))
		links = append(links,
			fmt.Sprintf(`{ output = "hrtf_%s:Out" input = "xtc_%s%s:In" }`, side, side, side),
			fmt.Sprintf(`{ output = "hrtf_%s:Out" input = "xtc_%s%s:In" }`, side, side, other),
			fmt.Sprintf(`{ output = "xtc_%s%s:Out" input = "spk_%s:In 1" }`, side, side, side),
			fmt.Sprintf(`{ output = "xtc_%s%s:Out" input = "spk_%s:In 2" }`, other, side, side),
			fmt.Sprintf(`{ output = "spk_%s:Out" input = "sur_%s:In 4" }`, side, side))
		outs[s] = "sur_" + side + ":Out"
	}
	return nodes, links, inputs, outs
}

// azimuth turns Sonar's speaker position, in degrees counterclockwise from
// the front, into the spatializer's 0 to 360.
func azimuth(position float64) float64 {
	a := math.Mod(position, 360)
	if a < 0 {
		a += 360
	}
	return a
}

// surroundParams are the controls of a surround channel's virtualizer.
func surroundParams(c channel, data *object) []any {
	if !c.surround || hrtfPath() == "" {
		return nil
	}
	on := dataBool(data, "globalEnableState") && dataBool(data, "virtualSurroundState")
	spk := dataPath(data, "formFactor") == "speakers"
	reverb := math.Pow(10, (math.Max(reverbGainMin, math.Min(0, dataFloat(data, "reverbGainDB")))+reverbOffsetDB)/20)
	var params []any
	for _, side := range []string{"l", "r"} {
		params = append(params, "sur_"+side+":Gain 1", boolParam(!on), "sur_"+side+":Gain 2", boolParam(on && !spk),
			"sur_"+side+":Gain 3", boolParam(on)*reverb, "sur_"+side+":Gain 4", boolParam(on && spk)*math.Pow(10, xtcLevelDB/20))
	}
	for i, sp := range speakers {
		pos := sp.angle
		if v := dataPath(data, "virtualSurroundChannels", sp.key, "position"); v != nil {
			pos = toFloat(v)
		}
		// A speaker's gain is how close it is: Sonar's distance slider
		// sets 0 dB at its farthest.
		gain := math.Max(0, math.Min(speakerGainMax, dataFloat(data, "virtualSurroundChannels", sp.key, "gain")))
		level := math.Pow(10, (hrtfLevelDB+gain)/20)
		if sp.pos == "LFE" {
			level *= lfeLevel
		}
		params = append(params, "vs_"+sp.pos+":Azimuth", azimuth(pos),
			fmt.Sprintf("hrtf_l:Gain %d", i+1), level, fmt.Sprintf("hrtf_r:Gain %d", i+1), level)
	}
	return params
}

// The reverb's room.

const (
	roomRT60 = 0.5 // seconds to decay by 60 dB
	roomRate = sampleRate
)

func roomPath() string { return filepath.Join(paths.State(), "sonar-room.wav") }

// roomIR is a small room's impulse response, a decorrelated pair with the
// energy of a unit impulse each: early reflections over the first 40 ms,
// then a diffuse tail that decays by 60 dB in roomRT60 and darkens as it
// does.
func roomIR() [2][]float32 {
	n := int(roomRT60 * roomRate)
	var out [2][]float32
	for ch := range out {
		r := rand.New(rand.NewSource(int64(ch + 1)))
		x := make([]float64, n)
		for range 12 {
			t := 0.005 + 0.035*r.Float64()
			sign := 1.0
			if r.Intn(2) == 0 {
				sign = -1
			}
			x[int(t*roomRate)] += sign * 0.5 * math.Exp(-6.9078*t/roomRT60)
		}
		var lp float64
		for i := int(0.015 * roomRate); i < n; i++ {
			t := float64(i) / roomRate
			cutoff := 8000 - 6000*t/roomRT60
			lp += (1 - math.Exp(-2*math.Pi*cutoff/roomRate)) * (r.NormFloat64() - lp)
			x[i] += 0.1 * lp * math.Exp(-6.9078*t/roomRT60)
		}
		var energy float64
		for _, v := range x {
			energy += v * v
		}
		out[ch] = make([]float32, n)
		for i, v := range x {
			out[ch][i] = float32(v / math.Sqrt(energy))
		}
	}
	return out
}

// Speaker mode's crosstalk canceller.

const (
	// The far ear hears a speaker at ±30° about 0.26 ms later than the near
	// one (Woodworth's model) and, in the band the head shadows, about 3 dB
	// quieter. Low frequencies are left alone, as RACE does: canceling them
	// would take far more level than speakers have.
	xtcDelay  = 12 // samples
	xtcGain   = 0.7
	xtcLowHz  = 250.0
	xtcHighHz = 5000.0
	xtcLength = 1024
)

func xtcPath() string { return filepath.Join(paths.State(), "sonar-xtc.wav") }

// xtcCrosstalk models the path from a speaker to the far ear, but for the
// delay: band-limited and attenuated. It returns a filter, fed one sample
// at a time.
func xtcCrosstalk() func(x float64) float64 {
	hpA := 1 / (1 + 2*math.Pi*xtcLowHz/roomRate)
	lpA := 1 - math.Exp(-2*math.Pi*xtcHighHz/roomRate)
	var hpY, hpX, lpY float64
	return func(x float64) float64 {
		hpY = hpA * (hpY + x - hpX)
		hpX = x
		lpY += lpA * (hpY - lpY)
		return xtcGain * lpY
	}
}

// xtcIR is the crosstalk canceller's response to an impulse on one side:
// on that side (direct) and on the other (cross). RACE subtracts from
// each side the other side's output, delayed and filtered as the
// crosstalk is, which cancels the crosstalk at the ears.
func xtcIR() (direct, cross []float32) {
	fromL, fromR := xtcCrosstalk(), xtcCrosstalk()
	l, r := make([]float64, xtcLength), make([]float64, xtcLength)
	for n := range xtcLength {
		in := 0.0
		if n == 0 {
			in = 1
		}
		var fl, fr float64 // the crossfeed of each side's output
		if n >= xtcDelay {
			fl, fr = fromL(l[n-xtcDelay]), fromR(r[n-xtcDelay])
		} else {
			fromL(0)
			fromR(0)
		}
		l[n], r[n] = in-fr, -fl
	}
	direct, cross = make([]float32, xtcLength), make([]float32, xtcLength)
	for n := range xtcLength {
		direct[n], cross[n] = float32(l[n]), float32(r[n])
	}
	return direct, cross
}

// writeSurroundFilters writes the reverb's and speaker mode's responses.
func writeSurroundFilters() error {
	room := roomIR()
	if err := writeIR(roomPath(), room[0], room[1]); err != nil {
		return err
	}
	direct, cross := xtcIR()
	return writeIR(xtcPath(), direct, cross)
}

// writeIR writes a pair of impulse responses as a 32-bit float WAV.
func writeIR(path string, a, b []float32) error {
	ir := [2][]float32{a, b}
	data := make([]byte, 0, len(ir[0])*8)
	for i := range ir[0] {
		data = binary.LittleEndian.AppendUint32(data, math.Float32bits(ir[0][i]))
		data = binary.LittleEndian.AppendUint32(data, math.Float32bits(ir[1][i]))
	}
	h := []byte("RIFF")
	h = binary.LittleEndian.AppendUint32(h, uint32(36+len(data)))
	h = append(h, "WAVEfmt "...)
	h = binary.LittleEndian.AppendUint32(h, 16)
	h = binary.LittleEndian.AppendUint16(h, 3) // IEEE float
	h = binary.LittleEndian.AppendUint16(h, 2)
	h = binary.LittleEndian.AppendUint32(h, roomRate)
	h = binary.LittleEndian.AppendUint32(h, roomRate*8)
	h = binary.LittleEndian.AppendUint16(h, 8)
	h = binary.LittleEndian.AppendUint16(h, 32)
	h = append(h, "data"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(len(data)))
	return os.WriteFile(path, append(h, data...), 0o644)
}
