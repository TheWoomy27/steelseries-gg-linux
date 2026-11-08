package sonar

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"ggl/internal/paths"
)

// Sonar's audio on PipeWire (docs/sonar.md). ggd runs one PipeWire client
// process (pipewire -c) hosting a filter-chain per channel. A playback
// channel's input is a virtual sink named like Sonar's device ("SteelSeries
// Sonar - Gaming"); its output plays to the device chosen for the channel.
// The microphone channel captures the chosen microphone and offers the
// result as a virtual source. EQ bands are bq_raw filters whose coefficients
// ggd sets while audio plays; volumes are the virtual devices' own.
//
// ggd links each channel to its device itself (link.go) instead of leaving
// it to the session manager: a missing device must leave the channel
// waiting rather than falling back to a default device, which may be
// Sonar's own.
//
// A filter-chain recreates its filters, at their configured coefficients,
// each time it starts running. The channels are therefore not passive, so
// they keep running while Sonar is on (as Sonar's devices do on Windows),
// and ggd reapplies a channel's settings whenever it starts running again,
// for example when its device returns.

type channel struct {
	role     string // Sonar's DeviceRole
	vad      int
	redirect string // the classic redirection that sets its device
	node     string // node.name of the virtual device
	desc     string
	capture  bool // the microphone channel
}

var channels = []channel{
	{"Game", vadGame, "Game", "sonar_game", "SteelSeries Sonar - Gaming", false},
	{"ChatRender", vadChatRender, "Chat", "sonar_chat", "SteelSeries Sonar - Chat", false},
	{"Media", vadMedia, "Media", "sonar_media", "SteelSeries Sonar - Media", false},
	{"Aux", vadAux, "Aux", "sonar_aux", "SteelSeries Sonar - Aux", false},
	{"ChatCapture", vadChatCapture, "Mic", "sonar_mic", "SteelSeries Sonar - Microphone", true},
}

func channelByRole(role string) (channel, bool) {
	for _, c := range channels {
		if strings.EqualFold(c.role, role) {
			return c, true
		}
	}
	return channel{}, false
}

// The device side of a channel: the playback stream of a playback channel,
// the capture stream of the microphone channel.
func (c channel) deviceStream() string { return c.node + ".device" }

const eqBands = 10

// Stage names in a channel's filter graph. Playback channels add Sonar's
// tone controls after the parametric EQ, and a mix stage for the master
// volume and ChatMix.
func (c channel) stages() []string {
	var s []string
	for i := 1; i <= eqBands; i++ {
		s = append(s, fmt.Sprintf("eq%d", i))
	}
	if !c.capture {
		s = append(s, "bass", "treble", "clarity")
	}
	s = append(s, "gain")
	if !c.capture {
		s = append(s, "mix")
	}
	return s
}

func isGainStage(name string) bool { return name == "gain" || name == "mix" }

// Streamer mode splits each playback channel into the monitoring mix (the
// user's headset) and the streaming mix, each with its own gain: the
// channel's device stream gets a second pair of ports. They are named as
// rear speakers because PipeWire drops AUX channels in conversion; ggd
// links them to the streaming device's front pair.
var streamPorts = map[string]string{"RL": "FL", "RR": "FR"}

// moduleConf is the filter-chain module for a channel; stream adds the
// streaming mix.
func (c channel) moduleConf(stream bool) string {
	var nodes, links []string
	stages := c.stages()
	for i, st := range stages {
		if isGainStage(st) {
			nodes = append(nodes, fmt.Sprintf(`{ type = builtin name = %s label = linear control = { "Mult" = 1.0 } }`, st))
		} else {
			nodes = append(nodes, fmt.Sprintf(`{ type = builtin name = %s label = bq_raw config = { coefficients = [ { rate = %d b0 = 1.0 b1 = 0.0 b2 = 0.0 a0 = 1.0 a1 = 0.0 a2 = 0.0 } ] } }`, st, sampleRate))
		}
		if i > 0 {
			links = append(links, fmt.Sprintf(`{ output = "%s:Out" input = "%s:In" }`, stages[i-1], st))
		}
	}
	position, channels := "[ FL FR ]", 2
	if c.capture {
		position, channels = "[ MONO ]", 1
	}
	devicePorts, ports := "", ""
	if stream && !c.capture {
		last := stages[len(stages)-1]
		nodes = append(nodes,
			`{ type = builtin name = mon label = linear control = { "Mult" = 0.0 } }`,
			`{ type = builtin name = str label = linear control = { "Mult" = 0.0 } }`)
		links = append(links,
			fmt.Sprintf(`{ output = "%s:Out" input = "mon:In" }`, last),
			fmt.Sprintf(`{ output = "%s:Out" input = "str:In" }`, last))
		ports = fmt.Sprintf(`
        inputs = [ "%s:In" ]
        outputs = [ "mon:Out" "str:Out" ]`, stages[0])
		devicePorts = " audio.channels = 4 audio.position = [ FL RL FR RR ]"
	}
	// The virtual device apps use, and the stream to or from the real
	// device, which ggd links.
	var virtual, device string
	if c.capture {
		virtual = fmt.Sprintf(`playback.props = { node.name = %q node.description = %q media.class = Audio/Source audio.rate = %d }`, c.node, c.desc, sampleRate)
		device = fmt.Sprintf(`capture.props = { node.name = %q node.description = %q node.autoconnect = false audio.rate = %d }`, c.deviceStream(), c.desc, sampleRate)
	} else {
		virtual = fmt.Sprintf(`capture.props = { node.name = %q node.description = %q media.class = Audio/Sink audio.rate = %d }`, c.node, c.desc, sampleRate)
		device = fmt.Sprintf(`playback.props = { node.name = %q node.description = %q node.autoconnect = false audio.rate = %d%s }`, c.deviceStream(), c.desc, sampleRate, devicePorts)
	}
	return fmt.Sprintf(`  { name = libpipewire-module-filter-chain
    args = {
      node.description = %q
      media.name = %q
      audio.channels = %d
      audio.position = %s
      filter.graph = {
        nodes = [
          %s
        ]
        links = [
          %s
        ]%s
      }
      %s
      %s
    }
  }
`, c.desc, c.desc, channels, position, strings.Join(nodes, "\n          "), strings.Join(links, "\n          "), ports, virtual, device)
}

// micCopyConf is streamer mode's copy of the processed microphone, to the
// monitoring mix (to hear oneself) and the streaming mix, each with its own
// gain (Sonar's MicCopy redirection).
const micCopy = "sonar_mic_copy"

func micCopyConf() string {
	return fmt.Sprintf(`  { name = libpipewire-module-filter-chain
    args = {
      node.description = "SteelSeries Sonar - Microphone copy"
      media.name = "SteelSeries Sonar - Microphone copy"
      filter.graph = {
        nodes = [
          { type = builtin name = in label = copy }
          { type = builtin name = mon label = linear control = { "Mult" = 0.0 } }
          { type = builtin name = str label = linear control = { "Mult" = 0.0 } }
        ]
        links = [
          { output = "in:Out" input = "mon:In" }
          { output = "in:Out" input = "str:In" }
        ]
        inputs = [ "in:In" ]
        outputs = [ "mon:Out" "str:Out" ]
      }
      capture.props = { node.name = %q node.autoconnect = false audio.rate = %d audio.channels = 1 audio.position = [ MONO ] }
      playback.props = { node.name = %q node.autoconnect = false audio.rate = %d audio.channels = 2 audio.position = [ FL RL ] }
    }
  }
`, micCopy, sampleRate, micCopy+".device", sampleRate)
}

// The Stream device, a virtual output for the streaming mix that streaming
// software records (its monitor). It is a loopback's input side: objects
// this process created itself would not reach the session's PipeWire.
const streamDevice = "sonar_stream"

func streamDeviceConf() string {
	return fmt.Sprintf(`  { name = libpipewire-module-loopback
    args = {
      node.description = "SteelSeries Sonar - Stream"
      audio.position = [ FL FR ]
      capture.props = { node.name = %q media.class = Audio/Sink audio.rate = %d }
      playback.props = { node.name = %q node.autoconnect = false node.passive = true audio.rate = %d }
    }
  }
`, streamDevice, sampleRate, streamDevice+".out", sampleRate)
}

func graphConf(chs []channel, stream bool) string {
	var b strings.Builder
	b.WriteString(`# Generated by ggd for Sonar.
context.properties = { log.level = 2 }
context.spa-libs = { audio.convert.* = audioconvert/libspa-audioconvert support.* = support/libspa-support }
context.modules = [
  { name = libpipewire-module-protocol-native }
  { name = libpipewire-module-client-node }
  { name = libpipewire-module-adapter }
`)
	for _, c := range chs {
		b.WriteString(c.moduleConf(stream))
	}
	if stream {
		b.WriteString(micCopyConf())
		b.WriteString(streamDeviceConf())
	}
	b.WriteString("]\n")
	return b.String()
}

// Parameters.

// dataPath reads a value from config data.
func dataPath(o *object, path ...string) any {
	var v any = o
	for _, k := range path {
		obj, ok := v.(*object)
		if !ok {
			return nil
		}
		v = obj.get(k)
	}
	return v
}

func dataBool(o *object, path ...string) bool { b, _ := dataPath(o, path...).(bool); return b }

func dataFloat(o *object, path ...string) float64 { return toFloat(dataPath(o, path...)) }

// Tone controls Sonar runs in its own (unpublished) DSP, as filters: the
// presets use them as dB amounts.
var toneFilters = map[string]struct {
	key  string
	typ  string
	freq float64
	q    float64
}{
	"bass":    {"bassBoostState", "lowShelving", 100, 0.7071},
	"treble":  {"trebleBoostState", "highShelving", 8000, 0.7071},
	"clarity": {"voiceClarityState", "peakingEQ", 2500, 1},
}

// channelParams are the filter-chain controls for a config's data. mix is
// the playback channels' master volume and ChatMix factor.
func channelParams(c channel, data *object, mix float64) []any {
	on := dataBool(data, "globalEnableState")
	eqOn := on && dataBool(data, "parametricEQ", "enabled")
	var params []any
	set := func(stage string, f biquad) {
		for _, kv := range []struct {
			k string
			v float64
		}{{"b0", f.b0}, {"b1", f.b1}, {"b2", f.b2}, {"a0", f.a0}, {"a1", f.a1}, {"a2", f.a2}} {
			params = append(params, stage+":"+kv.k, kv.v)
		}
	}
	for i := 1; i <= eqBands; i++ {
		band := fmt.Sprintf("filter%d", i)
		f := identity
		if eqOn && dataBool(data, "parametricEQ", band, "enabled") {
			f = newBiquad(fmt.Sprint(dataPath(data, "parametricEQ", band, "type")),
				dataFloat(data, "parametricEQ", band, "frequency"),
				dataFloat(data, "parametricEQ", band, "gain"),
				dataFloat(data, "parametricEQ", band, "qFactor"))
		}
		set(fmt.Sprintf("eq%d", i), f)
	}
	if !c.capture {
		for _, st := range []string{"bass", "treble", "clarity"} {
			t := toneFilters[st]
			f := identity
			if on && dataBool(data, t.key, "enabled") {
				f = newBiquad(t.typ, t.freq, dataFloat(data, t.key, "value"), t.q)
			}
			set(st, f)
		}
	}
	gain := 1.0
	if on {
		gain = math.Pow(10, dataFloat(data, "generalGain")/20)
	}
	params = append(params, "gain:Mult", gain)
	if !c.capture {
		params = append(params, "mix:Mult", mix)
	}
	return params
}

// podParams formats filter-chain controls for pw-cli.
func podParams(params []any) string {
	var b strings.Builder
	b.WriteString("{ params = [")
	for i := 0; i < len(params); i += 2 {
		fmt.Fprintf(&b, " %q %g", params[i], params[i+1])
	}
	b.WriteString(" ] }")
	return b.String()
}

// sliderGain maps Sonar's 0..1 volume sliders to a linear gain, with the
// cubic taper PipeWire's own tools use.
func sliderGain(v float64) float64 {
	v = math.Max(0, math.Min(1, v))
	return v * v * v
}

// The engine.

type audio struct {
	relinking sync.Mutex // one relink at a time
	mu        sync.Mutex
	cmd       *exec.Cmd
	running   []channel
	ids       map[string]int    // node ids by node.name
	targets   map[string]string // device node.name by redirection
	linked    map[string]bool   // channels linked to their device, by node
	active    map[string]bool   // channels running, by node
	stopped   chan struct{}     // closed when the graph stops

	stream    bool            // streamer mode's graph
	micSample string          // a player feeding the microphone channel, by node.name
	monitor   bool            // play the processed microphone on the chat device
	extra     map[[2]int]bool // links ggd made between other nodes (see relinkExtras)
	copyOn    bool            // the microphone copy is running

	// onRunning is called when a channel starts running, to reapply its
	// settings, and onCopyRunning when the microphone copy does.
	onRunning     func(c channel)
	onCopyRunning func()
}

// setMicSample feeds a player into the microphone channel, beside the
// microphone, and monitors the channel on the chat device; "" stops both.
func (a *audio) setMicSample(node string) {
	a.mu.Lock()
	a.micSample, a.monitor = node, node != ""
	a.mu.Unlock()
	go a.relink()
}

func pwCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = os.Environ()
	return cmd
}

// nodeIDs maps node.name to id for the nodes in the graph.
func nodeIDs() (map[string]int, error) {
	objs, err := pwDump()
	if err != nil {
		return nil, err
	}
	ids := map[string]int{}
	for _, o := range objs {
		if o.Type == "PipeWire:Interface:Node" {
			if n, _ := o.Info.Props["node.name"].(string); n != "" {
				ids[n] = o.ID
			}
		}
	}
	return ids, nil
}

// start runs the graph for chs, linking each channel to its device from
// targets (device node names by redirection).
func (a *audio) start(chs []channel, targets map[string]string, stream bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopLocked()
	conf := filepath.Join(paths.State(), "sonar-pipewire.conf")
	if err := os.WriteFile(conf, []byte(graphConf(chs, stream)), 0o644); err != nil {
		return err
	}
	a.stream, a.extra, a.copyOn = stream, map[[2]int]bool{}, false
	cmd := pwCommand("pipewire", "-c", conf)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("pipewire: %w", err)
	}
	a.cmd, a.running = cmd, chs
	go cmd.Wait()
	// Wait for every channel's nodes to appear.
	for range 50 {
		time.Sleep(100 * time.Millisecond)
		ids, err := nodeIDs()
		if err != nil {
			continue
		}
		ready := true
		for _, c := range chs {
			if _, ok := ids[c.node]; !ok {
				ready = false
			}
		}
		for _, n := range []string{micCopy, streamDevice} {
			if _, ok := ids[n]; stream && !ok {
				ready = false
			}
		}
		if ready {
			a.ids, a.targets, a.linked, a.active = ids, targets, map[string]bool{}, map[string]bool{}
			a.stopped = make(chan struct{})
			go a.keepLinked(a.stopped)
			return nil
		}
	}
	a.stopLocked()
	return fmt.Errorf("sonar audio graph did not come up")
}

func (a *audio) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopLocked()
}

func (a *audio) stopLocked() {
	if a.cmd != nil && a.cmd.Process != nil {
		a.cmd.Process.Signal(syscall.SIGTERM)
	}
	if a.stopped != nil {
		close(a.stopped)
	}
	a.cmd, a.running, a.ids, a.targets, a.linked, a.active, a.stopped = nil, nil, nil, nil, nil, nil, nil
}

func (a *audio) isRunning(c channel) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range a.running {
		if r.node == c.node {
			return true
		}
	}
	return false
}

// nodeID finds a node, reading the graph again for nodes that appeared
// after the last read.
func (a *audio) nodeID(name string) (int, bool) {
	a.mu.Lock()
	id, ok := a.ids[name]
	a.mu.Unlock()
	if ok {
		return id, true
	}
	ids, err := nodeIDs()
	if err != nil {
		return 0, false
	}
	a.mu.Lock()
	if a.ids != nil {
		a.ids = ids
	}
	a.mu.Unlock()
	id, ok = ids[name]
	return id, ok
}

func (a *audio) setParam(node, pod string) {
	id, ok := a.nodeID(node)
	if !ok {
		return
	}
	if out, err := pwCommand("pw-cli", "set-param", fmt.Sprint(id), "Props", pod).CombinedOutput(); err != nil {
		slog.Warn("sonar: setting audio parameters", "node", node, "err", err, "out", strings.TrimSpace(string(out)))
	}
}

// setControls sets a channel's filter controls.
func (a *audio) setControls(c channel, params []any) {
	// The controls live on the filter-chain's input side.
	node := c.node
	if c.capture {
		node = c.deviceStream()
	}
	a.setParam(node, podParams(params))
}

// setVolume sets a channel's virtual device volume.
func (a *audio) setVolume(c channel, gain float64, mute bool) {
	vols := fmt.Sprintf("%g %g", gain, gain)
	if c.capture {
		vols = fmt.Sprintf("%g", gain)
	}
	a.setParam(c.node, fmt.Sprintf("{ mute = %t channelVolumes = [ %s ] }", mute, vols))
}

// setTarget moves a redirection's channels to another device.
// setTargets replaces every device the graph plays to or records from.
func (a *audio) setTargets(targets map[string]string) {
	a.mu.Lock()
	if a.targets == nil {
		a.mu.Unlock()
		return
	}
	a.targets = targets
	a.mu.Unlock()
	a.relink()
}

func (a *audio) setTarget(redirect, target string) {
	a.mu.Lock()
	if a.targets == nil {
		a.mu.Unlock()
		return
	}
	a.targets[redirect] = target
	a.mu.Unlock()
	a.relink()
}

// isLinked reports whether a channel plays to (or captures from) its device.
func (a *audio) isLinked(c channel) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.linked[c.node]
}

// Default devices.

// defaultDevices reads the configured default sink and source.
func defaultDevices() (sink, source string, err error) {
	out, err := pwCommand("pw-metadata", "0").Output()
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		name := func(key string) (string, bool) {
			if !strings.Contains(line, "key:'"+key+"'") {
				return "", false
			}
			i := strings.Index(line, "value:'")
			if i < 0 {
				return "", false
			}
			var v struct {
				Name string `json:"name"`
			}
			json.Unmarshal([]byte(strings.TrimSuffix(line[i+len("value:'"):], "' type:'Spa:String:JSON'")), &v)
			return v.Name, true
		}
		if n, ok := name("default.configured.audio.sink"); ok {
			sink = n
		}
		if n, ok := name("default.configured.audio.source"); ok {
			source = n
		}
	}
	return sink, source, nil
}

func setDefaultDevice(key, node string) error {
	args := []string{"0", key, fmt.Sprintf(`{ "name": %q }`, node), "Spa:String:JSON"}
	if node == "" {
		args = []string{"-d", "0", key}
	}
	return pwCommand("pw-metadata", args...).Run()
}
