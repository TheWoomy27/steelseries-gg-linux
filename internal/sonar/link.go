package sonar

import (
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"
)

// ggd links each Sonar channel's device stream to the device chosen for it
// and keeps it linked as devices come and go. A channel whose device is
// missing stays unlinked until the device returns.

type pwPort struct {
	id, node int
	output   bool
	channel  string // audio.channel: FL, FR, MONO...
	monitor  bool
}

type pwLink struct{ id, out, in int }

// readGraph reads nodes (ids and states by name), ports and links.
func readGraph() (nodes map[string]int, states map[string]string, ports []pwPort, links []pwLink, err error) {
	objs, err := pwDump()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	nodes, states = map[string]int{}, map[string]string{}
	for _, o := range objs {
		p := o.Info.Props
		switch o.Type {
		case "PipeWire:Interface:Node":
			if n, _ := p["node.name"].(string); n != "" {
				nodes[n], states[n] = o.ID, o.Info.State
			}
		case "PipeWire:Interface:Port":
			node, _ := p["node.id"].(float64)
			ch, _ := p["audio.channel"].(string)
			mon, _ := p["port.monitor"].(bool)
			ports = append(ports, pwPort{o.ID, int(node), o.Info.Direction == "output", ch, mon})
		case "PipeWire:Interface:Link":
			links = append(links, pwLink{o.ID, o.Info.OutputPort, o.Info.InputPort})
		}
	}
	return nodes, states, ports, links, nil
}

func portsOf(ports []pwPort, node int, output bool) []pwPort {
	var out []pwPort
	for _, p := range ports {
		if p.node == node && p.output == output && !p.monitor {
			out = append(out, p)
		}
	}
	return out
}

// sourcePorts are a capture device's outputs. Virtual sources (a null sink
// offered as a microphone) only have monitor ports.
func sourcePorts(ports []pwPort, node int) []pwPort {
	if out := portsOf(ports, node, true); len(out) > 0 {
		return out
	}
	var out []pwPort
	for _, p := range ports {
		if p.node == node && p.output {
			out = append(out, p)
		}
	}
	return out
}

// pairPorts matches output ports to input ports by channel position. A
// single input port (a mono device, or the microphone channel) takes every
// output, and a single output feeds every input; inputs without a matching
// channel (a surround device's rear speakers) stay unlinked.
func pairPorts(from, to []pwPort) [][2]int {
	byChannel := map[string]int{}
	for _, f := range from {
		byChannel[f.channel] = f.id
	}
	var out [][2]int
	for _, t := range to {
		switch id, ok := byChannel[t.channel]; {
		case ok:
			out = append(out, [2]int{id, t.id})
		case len(to) == 1:
			for _, f := range from {
				out = append(out, [2]int{f.id, t.id})
			}
		case len(from) == 1:
			out = append(out, [2]int{from[0].id, t.id})
		}
	}
	return out
}

// relink makes each running channel's links match its device, and
// reapplies the settings of channels that started running.
//
// In streamer mode a playback channel's monitoring ports go to the
// monitoring device and its streaming ports (see streamPorts) to the
// streaming device; the targets map holds "Monitoring", "Streaming" and,
// while the user listens to the stream, "Listen".
func (a *audio) relink() {
	a.relinking.Lock()
	defer a.relinking.Unlock()
	a.mu.Lock()
	running := a.running
	targets := map[string]string{}
	for k, v := range a.targets {
		targets[k] = v
	}
	stream, micSample, monitor, extra, aecOn := a.stream, a.micSample, a.monitor, a.extra, a.aec
	a.mu.Unlock()
	if running == nil {
		return
	}
	nodes, states, ports, links, err := readGraph()
	if err != nil {
		slog.Warn("sonar: reading the audio graph", "err", err)
		return
	}
	// Echo canceling runs while it is on and Sonar plays to speakers.
	speakers := ""
	if objs, err := pwDump(); err == nil {
		speakers = aecSpeakers(targets, objs)
	}
	aecSrc, ok1 := nodes[aecSource]
	aecIn, ok2 := nodes[aecCapture]
	aecRef, ok3 := nodes[aecReference]
	spk, ok4 := nodes[speakers]
	aecRun := aecOn && ok1 && ok2 && ok3 && ok4 && speakers != ""
	device := func(target string) (int, bool) {
		id, ok := nodes[targets[target]]
		return id, ok && targets[target] != ""
	}
	linked, active := map[string]bool{}, map[string]bool{}
	for _, c := range running {
		node, ok := nodes[c.deviceStream()]
		if !ok {
			continue
		}
		// The stream's side facing the device.
		own := portsOf(ports, node, !c.capture)
		ownIDs := map[int]bool{}
		mon, str := own, []pwPort(nil)
		if stream && !c.capture {
			mon = nil
			for _, p := range own {
				if ch, ok := streamPorts[p.channel]; ok {
					p.channel = ch
					str = append(str, p)
				} else {
					mon = append(mon, p)
				}
			}
		}
		for _, p := range own {
			ownIDs[p.id] = true
		}
		want := map[[2]int]bool{}
		if dev, ok := device(c.redirect); ok {
			pairs := pairPorts(mon, portsOf(ports, dev, false))
			if c.capture {
				pairs = pairPorts(sourcePorts(ports, dev), own)
				if aecRun {
					pairs = pairPorts(portsOf(ports, aecSrc, true), own)
				}
			}
			for _, p := range pairs {
				want[p] = true
			}
		}
		if dev, ok := device("Streaming"); ok && len(str) > 0 {
			for _, p := range pairPorts(str, portsOf(ports, dev, false)) {
				want[p] = true
			}
		}
		if sample, ok := nodes[micSample]; ok && c.capture && micSample != "" {
			for _, p := range pairPorts(portsOf(ports, sample, true), own) {
				want[p] = true
			}
		}
		have := 0
		for _, l := range links {
			if !ownIDs[l.out] && !ownIDs[l.in] {
				continue
			}
			if want[[2]int{l.out, l.in}] {
				delete(want, [2]int{l.out, l.in})
				have++
				continue
			}
			pwLinkCmd("-d", fmt.Sprint(l.id))
		}
		for p := range want {
			if pwLinkCmd(fmt.Sprint(p[0]), fmt.Sprint(p[1])) {
				have++
			}
		}
		linked[c.node] = have > 0
		active[c.node] = states[c.deviceStream()] == "running"
	}

	// Links between other nodes: the processed microphone monitored on the
	// chat device while the mic sample plays, and in streamer mode the
	// microphone copy and listening to the stream.
	want := map[[2]int]bool{}
	add := func(pairs [][2]int) {
		for _, p := range pairs {
			want[p] = true
		}
	}
	if dev, ok := device("Mic"); ok && aecRun {
		// The microphone into the echo canceller, and the speakers'
		// monitor as what to remove.
		add(pairPorts(sourcePorts(ports, dev), portsOf(ports, aecIn, false)))
		var monitors []pwPort
		for _, p := range ports {
			if p.node == spk && p.output && p.monitor {
				monitors = append(monitors, p)
			}
		}
		add(pairPorts(monitors, portsOf(ports, aecRef, false)))
	}
	mic, haveMic := nodes["sonar_mic"]
	if dev, ok := device("Chat"); ok && monitor && haveMic {
		add(pairPorts(sourcePorts(ports, mic), portsOf(ports, dev, false)))
	}
	if stream {
		if in, ok := nodes[micCopy]; ok && haveMic {
			add(pairPorts(sourcePorts(ports, mic), portsOf(ports, in, false)))
		}
		if out, ok := nodes[micCopy+".device"]; ok {
			for _, mix := range []struct{ port, target string }{{"FL", "Monitoring"}, {"RL", "Streaming"}} {
				dev, ok := device(mix.target)
				if !ok {
					continue
				}
				for _, p := range portsOf(ports, out, true) {
					if p.channel == mix.port {
						p.channel = "MONO"
						add(pairPorts([]pwPort{p}, portsOf(ports, dev, false)))
					}
				}
			}
		}
		if listen, ok := device("Listen"); ok {
			if str, ok := device("Streaming"); ok {
				var monitors []pwPort
				for _, p := range ports {
					if p.node == str && p.output && p.monitor {
						monitors = append(monitors, p)
					}
				}
				add(pairPorts(monitors, portsOf(ports, listen, false)))
			}
		}
	}
	extra = relinkExtras(want, links, extra)
	copyOn := states[micCopy] == "running"

	a.mu.Lock()
	var started []channel
	copyStarted := false
	if a.running != nil {
		for _, c := range running {
			if active[c.node] && !a.active[c.node] {
				started = append(started, c)
			}
		}
		copyStarted = copyOn && !a.copyOn
		a.linked, a.active, a.extra, a.copyOn = linked, active, extra, copyOn
	}
	aecChanged := (a.speakers != "") != (speakers != "")
	a.speakers = speakers
	onRunning, onCopyRunning, onAEC := a.onRunning, a.onCopyRunning, a.onAEC
	a.mu.Unlock()
	if aecChanged && onAEC != nil {
		onAEC(speakers != "")
	}
	for _, c := range started {
		if onRunning != nil {
			onRunning(c)
		}
	}
	if copyStarted && onCopyRunning != nil {
		onCopyRunning()
	}
}

// relinkExtras makes the links ggd manages between other nodes match want:
// it removes the ones it made before that are no longer wanted and makes
// the missing ones. It returns the links it now manages.
func relinkExtras(want map[[2]int]bool, links []pwLink, before map[[2]int]bool) map[[2]int]bool {
	missing := maps.Clone(want)
	for _, l := range links {
		pair := [2]int{l.out, l.in}
		switch {
		case want[pair]:
			delete(missing, pair)
		case before[pair]:
			pwLinkCmd("-d", fmt.Sprint(l.id))
		}
	}
	for p := range missing {
		pwLinkCmd(fmt.Sprint(p[0]), fmt.Sprint(p[1]))
	}
	return want
}

func pwLinkCmd(args ...string) bool {
	if out, err := pwCommand("pw-link", args...).CombinedOutput(); err != nil {
		slog.Warn("sonar: pw-link", "args", args, "err", err, "out", strings.TrimSpace(string(out)))
		return false
	}
	return true
}

// keepLinked relinks the channels until stopped, following devices that
// appear and disappear.
func (a *audio) keepLinked(stopped chan struct{}) {
	a.relink()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-stopped:
			return
		case <-t.C:
			a.relink()
		}
	}
}
