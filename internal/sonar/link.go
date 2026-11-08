package sonar

import (
	"fmt"
	"log/slog"
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
// output; inputs without a matching channel (a surround device's rear
// speakers) stay unlinked.
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
		}
	}
	return out
}

// relink makes each running channel's links match its device, and
// reapplies the settings of channels that started running.
func (a *audio) relink() {
	a.mu.Lock()
	running := a.running
	targets := map[string]string{}
	for k, v := range a.targets {
		targets[k] = v
	}
	a.mu.Unlock()
	if running == nil {
		return
	}
	nodes, states, ports, links, err := readGraph()
	if err != nil {
		slog.Warn("sonar: reading the audio graph", "err", err)
		return
	}
	linked, active := map[string]bool{}, map[string]bool{}
	for _, c := range running {
		stream, ok := nodes[c.deviceStream()]
		if !ok {
			continue
		}
		// The stream's side facing the device.
		own := portsOf(ports, stream, !c.capture)
		ownIDs := map[int]bool{}
		for _, p := range own {
			ownIDs[p.id] = true
		}
		want := map[[2]int]bool{}
		if dev, ok := nodes[targets[c.redirect]]; ok && targets[c.redirect] != "" {
			pairs := pairPorts(own, portsOf(ports, dev, false))
			if c.capture {
				pairs = pairPorts(sourcePorts(ports, dev), own)
			}
			for _, p := range pairs {
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
	a.mu.Lock()
	var started []channel
	if a.running != nil {
		for _, c := range running {
			if active[c.node] && !a.active[c.node] {
				started = append(started, c)
			}
		}
		a.linked, a.active = linked, active
	}
	onRunning := a.onRunning
	a.mu.Unlock()
	for _, c := range started {
		if onRunning != nil {
			onRunning(c)
		}
	}
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
