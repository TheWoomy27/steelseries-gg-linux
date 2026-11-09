package sonar

import (
	"encoding/binary"
	"io"
	"log/slog"
	"math"
	"os/exec"
	"slices"
	"strconv"
	"sync"
)

// noiseFloor measures the raw microphone's noise floor for the automatic
// noise gate. A PipeWire stream in peak mode (as GG's audio meters read
// levels) gives the peak of every 40 ms; the floor is their 30th percentile
// over the last ten seconds of sound, which speech, louder and rarely most
// of the time, leaves alone.
type noiseFloor struct {
	mu      sync.Mutex
	device  string
	cmd     *exec.Cmd
	peaks   []float64 // dBFS
	seen    int       // peaks since following the device
	floor   float64   // NaN until measured
	changed func()    // called when the floor moves
}

const (
	floorRate   = 25
	floorWindow = 10 * floorRate
	floorFirst  = 2 * floorRate // peaks before the first estimate
	floorStepDB = 1.0           // smaller moves are not applied
)

func newNoiseFloor() *noiseFloor { return &noiseFloor{floor: math.NaN()} }

// get returns the floor in dBFS, or NaN.
func (n *noiseFloor) get() float64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.floor
}

// follow measures a device's noise floor, "" for none.
func (n *noiseFloor) follow(device string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if device == n.device {
		return
	}
	if n.cmd != nil {
		n.cmd.Process.Kill()
		n.cmd = nil
	}
	n.device, n.peaks, n.seen, n.floor = device, nil, 0, math.NaN()
	if device == "" {
		return
	}
	// pw-record's output is buffered on a pipe; stdbuf passes each peak on
	// as it is measured. Without a target pw-record would take the default
	// device, so it stops instead when the microphone goes away.
	cmd := pwCommand("stdbuf", "-o0", "pw-record", "-a", "--rate", strconv.Itoa(floorRate), "--channels", "1", "--format", "f32",
		"--target", device, "-P", "{ resample.peaks = true node.passive = true stream.monitor = true node.dont-reconnect = true node.name = sonar_noise_floor }", "-")
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		slog.Warn("sonar: measuring the microphone's noise floor", "err", err)
		n.device = ""
		return
	}
	n.cmd = cmd
	go n.read(cmd, out)
}

func (n *noiseFloor) read(cmd *exec.Cmd, r io.Reader) {
	buf := make([]byte, 4)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			break
		}
		peak := math.Abs(float64(math.Float32frombits(binary.LittleEndian.Uint32(buf))))
		n.mu.Lock()
		if n.cmd != cmd {
			n.mu.Unlock()
			break
		}
		// Digital silence (a muted microphone) is not noise.
		if peak == 0 {
			n.mu.Unlock()
			continue
		}
		n.peaks = append(n.peaks, 20*math.Log10(peak))
		if len(n.peaks) > floorWindow {
			n.peaks = n.peaks[len(n.peaks)-floorWindow:]
		}
		n.seen++
		moved := false
		if n.seen >= floorFirst && n.seen%floorRate == 0 {
			if f := percentile(n.peaks, 0.3); math.IsNaN(n.floor) || math.Abs(f-n.floor) >= floorStepDB {
				n.floor, moved = f, true
			}
		}
		changed := n.changed
		n.mu.Unlock()
		if moved && changed != nil {
			changed()
		}
	}
	cmd.Wait()
	// The microphone went away: follow starts again when asked.
	n.mu.Lock()
	if n.cmd == cmd {
		n.cmd, n.device = nil, ""
	}
	n.mu.Unlock()
}

func percentile(v []float64, p float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	return s[int(p*float64(len(s)-1))]
}
