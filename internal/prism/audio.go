package prism

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"math/cmplx"
	"os/exec"
	"sort"
	"sync"
)

// Audio for the Audio Visualizer effect: the sound going to an output
// (the default one unless the user picks another), recorded from its
// monitor with pw-record while a configuration uses the effect. Windows
// Prism gets band levels from a sampling library; here they come from an
// FFT of the latest samples, in log-spaced bands, then go through
// Prism's own normalisation: "tide marks" following the quietest and
// loudest bands, an amplitude multiplier from the user's amplitude
// setting, and averaging with the previous frame.

const (
	audioRate  = 48000
	fftSize    = 2048
	bandLowHz  = 40
	bandHighHz = 16000
)

// avFrame is one frame of audio levels: per band for each device bitmap
// width, and the peak level (0-1).
type avFrame struct {
	bands map[int][]float32
	level float32
}

type audioCapture struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	target  string
	running bool
	ring    [fftSize]float32 // mono, latest last
	pos     int
	peakL   float32
	peakR   float32

	// Prism's sampling state (sampling.AudioSampleService).
	scalar    float32 // the amplitude setting: 3 high, 1.25 medium, 0.5 low
	low, high float32 // tide marks
	rng, mult float32
	prev      map[int][]float32
}

// need starts or stops recording, from target ("" for the default
// output).
func (a *audioCapture) need(on bool, target string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if on && a.running && a.target == target {
		return
	}
	if a.cmd != nil {
		a.cmd.Process.Kill()
		a.cmd = nil
	}
	a.running = false
	if !on {
		return
	}
	args := []string{"--raw", "--format", "f32", "--rate", "48000", "--channels", "2", "--latency", "20ms",
		"-P", "{ stream.capture.sink=true node.name=gg-prism-audio-visualizer node.description=\"SteelSeries GG Audio Visualizer\" }"}
	if target != "" {
		args = append(args, "--target", target)
	}
	cmd := exec.Command("pw-record", append(args, "-")...)
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		slog.Warn("prism: audio visualizer can't record", "err", err)
		return
	}
	a.cmd, a.target, a.running = cmd, target, true
	slog.Info("prism: audio visualizer listening", "output", orDefault(target))
	go a.read(cmd, out)
}

func orDefault(s string) string {
	if s == "" {
		return "default"
	}
	return s
}

func (a *audioCapture) read(cmd *exec.Cmd, out io.Reader) {
	defer cmd.Wait()
	r := bufio.NewReaderSize(out, 16384)
	frame := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, frame); err != nil {
			break
		}
		l := math.Float32frombits(binary.LittleEndian.Uint32(frame[0:]))
		rt := math.Float32frombits(binary.LittleEndian.Uint32(frame[4:]))
		a.mu.Lock()
		if a.cmd != cmd {
			a.mu.Unlock()
			return
		}
		a.ring[a.pos] = (l + rt) / 2
		a.pos = (a.pos + 1) % fftSize
		a.peakL = max(a.peakL, abs32(l))
		a.peakR = max(a.peakR, abs32(rt))
		a.mu.Unlock()
	}
	a.mu.Lock()
	if a.cmd == cmd {
		a.cmd, a.running = nil, false
		slog.Info("prism: audio visualizer recording ended")
	}
	a.mu.Unlock()
}

func abs32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

// frame analyses the latest samples into levels for the device widths.
func (a *audioCapture) frame(widths []int, scalar float32) *avFrame {
	a.mu.Lock()
	samples := make([]float64, fftSize)
	for i := range samples {
		samples[i] = float64(a.ring[(a.pos+i)%fftSize])
	}
	peakL, peakR := a.peakL, a.peakR
	a.peakL, a.peakR = 0, 0
	a.mu.Unlock()
	mags := spectrum(samples)
	raw := map[int][]float32{}
	var lo, hi float32
	for _, w := range widths {
		b := bands(mags, w)
		raw[w] = b
		for _, v := range b {
			if v != 0 && (lo == 0 || v < lo) {
				lo = v
			}
			hi = max(hi, v)
		}
	}
	return a.normalise(raw, lo, hi, peakL, peakR, scalar)
}

// normalise is Prism's GetAudioSample after the sampling library: tide
// marks, the amplitude multiplier and averaging with the last frame.
func (a *audioCapture) normalise(raw map[int][]float32, lo, hi, peakL, peakR, scalar float32) *avFrame {
	a.scalar = scalar
	a.updateTideMarks(lo, hi)
	out := &avFrame{bands: map[int][]float32{}}
	for w, b := range raw {
		v := make([]float32, len(b))
		copy(v, b)
		if a.rng > 0 {
			prev := a.prev[w]
			for i := range v {
				x := (v[i] - a.low) / a.rng * a.mult
				p := x
				if i < len(prev) {
					p = prev[i]
				}
				v[i] = x*0.5 + p*0.5
			}
		}
		out.bands[w] = v
	}
	a.prev = out.bands
	out.level = (peakL*a.mult + peakR*a.mult) * 0.5
	return out
}

// updateTideMarks follows the quietest band (snapping down, rising
// slowly) and the loudest (snapping up, falling slowly).
func (a *audioCapture) updateTideMarks(lo, hi float32) {
	if lo != 0 && !math.IsNaN(float64(lo)) {
		t := lo * 1.15
		if a.low == 0 || a.low > t {
			a.low = t
		} else {
			a.low = a.low*0.95 + t*0.05
		}
	}
	if hi != 0 && !math.IsNaN(float64(hi)) {
		t := hi * 0.93
		if a.high == 0 || t > a.high {
			a.high = t
		} else {
			a.high = a.high*0.95 + t*0.05
		}
	}
	if a.scalar == 0 {
		a.scalar = 3
	}
	if a.high > 0 {
		a.mult = (2*float32(math.Log10(float64(a.high))) + a.scalar) / a.scalar
	}
	a.rng = a.high - a.low
}

// spectrum is the magnitude of each FFT bin of a Hann-windowed frame,
// where a full-scale sine reads 1.
func spectrum(samples []float64) []float64 {
	n := len(samples)
	x := make([]complex128, n)
	var sum float64
	for i, s := range samples {
		w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
		sum += w
		x[i] = complex(s*w, 0)
	}
	fft(x)
	mags := make([]float64, n/2)
	for i := range mags {
		mags[i] = 2 * cmplx.Abs(x[i]) / sum
	}
	return mags
}

// fft is an in-place radix-2 FFT (len(x) a power of two).
func fft(x []complex128) {
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		step := cmplx.Exp(complex(0, -2*math.Pi/float64(size)))
		for start := 0; start < n; start += size {
			w := complex(1, 0)
			for k := 0; k < size/2; k++ {
				u, v := x[start+k], x[start+k+size/2]*w
				x[start+k], x[start+k+size/2] = u+v, u-v
				w *= step
			}
		}
	}
}

// bands splits a spectrum into n log-spaced bands from 40 Hz to 16 kHz:
// each band's loudest bin, on a 60 dB scale (0 silent, 1 full scale).
func bands(mags []float64, n int) []float32 {
	out := make([]float32, n)
	if n <= 0 {
		return out
	}
	binHz := float64(audioRate) / float64(2*len(mags))
	edge := func(i int) float64 {
		return bandLowHz * math.Pow(bandHighHz/bandLowHz, float64(i)/float64(n))
	}
	for i := 0; i < n; i++ {
		from, to := int(edge(i)/binHz), int(edge(i+1)/binHz)
		to = max(to, from+1)
		var m float64
		for k := from; k < to && k < len(mags); k++ {
			m = max(m, mags[k])
		}
		if m > 0 {
			db := 20 * math.Log10(m)
			out[i] = float32(min(max(1+db/60, 0), 1))
		}
	}
	return out
}

// previewFrame is a made-up frame for GG's previews of the effect, as
// Windows Prism plays recorded samples into them: waves rolling across
// the bands.
func previewFrame(widths []int, t int64) *avFrame {
	f := &avFrame{bands: map[int][]float32{}}
	s := float64(t) / 1000
	for _, w := range widths {
		b := make([]float32, w)
		for i := range b {
			x := float64(i) / float64(max(w-1, 1))
			v := 0.55 + 0.3*math.Sin(2*math.Pi*(x*1.5-s*0.8)) + 0.15*math.Sin(2*math.Pi*(x*3.7+s*1.3))
			b[i] = float32(v * (1 - 0.35*x))
		}
		f.bands[w] = b
	}
	f.level = float32(0.5 + 0.35*math.Sin(2*math.Pi*s*0.9))
	return f
}

// outputs lists the outputs the effect can listen to, for GG's audio
// source menu.
func outputs() []map[string]any {
	out := []map[string]any{}
	b, err := exec.Command("pw-dump").Output()
	if err != nil {
		return out
	}
	var objs []struct {
		Type string `json:"type"`
		Info struct {
			Props map[string]any `json:"props"`
		} `json:"info"`
	}
	if json.Unmarshal(b, &objs) != nil {
		return out
	}
	for _, o := range objs {
		p := o.Info.Props
		if o.Type != "PipeWire:Interface:Node" || p["media.class"] != "Audio/Sink" {
			continue
		}
		name, _ := p["node.name"].(string)
		desc, _ := p["node.description"].(string)
		if name == "" {
			continue
		}
		if desc == "" {
			desc = name
		}
		out = append(out, map[string]any{"id": name, "friendlyName": desc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["friendlyName"].(string) < out[j]["friendlyName"].(string) })
	return out
}
