package sonar

import (
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// The PipeWire graph, kept current by one `pw-dump --monitor` rather than
// a full pw-dump at every look: Sonar looks every second, and a full dump
// of a desktop's graph is well over a megabyte of JSON.
//
// Right after Sonar changes the graph itself (any PipeWire command), a
// look runs a real pw-dump again for a moment, so code checking its own
// change never sees the monitor lag behind.

type graphModel struct {
	mu      sync.Mutex
	objs    map[int]pwObject
	ready   bool      // the first full dump has arrived
	changed time.Time // when Sonar last ran a PipeWire command
}

var (
	graph     = &graphModel{objs: map[int]pwObject{}}
	graphOnce sync.Once
)

// freshAfterCommand is how long after a PipeWire command looks run a real
// pw-dump.
const freshAfterCommand = 1500 * time.Millisecond

// startGraphMonitor starts following the graph, once per process.
func startGraphMonitor() {
	graphOnce.Do(func() { go graph.run() })
}

// touch notes that Sonar is changing the graph.
func (g *graphModel) touch() {
	g.mu.Lock()
	g.changed = time.Now()
	g.mu.Unlock()
}

func (g *graphModel) run() {
	for {
		g.follow()
		g.mu.Lock()
		g.ready = false
		g.objs = map[int]pwObject{}
		g.mu.Unlock()
		time.Sleep(2 * time.Second)
	}
}

// follow runs one monitor until it exits.
func (g *graphModel) follow() {
	cmd := pwCommand("pw-dump", "--monitor")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		slog.Debug("pw-dump --monitor", "err", err)
		return
	}
	defer cmd.Wait()
	defer cmd.Process.Kill()
	dec := json.NewDecoder(out)
	for {
		var batch []json.RawMessage
		if err := dec.Decode(&batch); err != nil {
			slog.Debug("pw-dump --monitor ended", "err", err)
			return
		}
		g.apply(batch)
	}
}

// apply takes one batch of the monitor: objects added or changed (whole),
// and removed ones ({"id": N, "info": null}).
func (g *graphModel) apply(batch []json.RawMessage) {
	parsed := make([]pwObject, 0, len(batch))
	var removed []int
	for _, raw := range batch {
		var head struct {
			ID   int             `json:"id"`
			Info json.RawMessage `json:"info"`
		}
		if json.Unmarshal(raw, &head) != nil {
			continue
		}
		if len(head.Info) == 0 || string(head.Info) == "null" {
			removed = append(removed, head.ID)
			continue
		}
		var o pwObject
		if json.Unmarshal(raw, &o) == nil {
			parsed = append(parsed, o)
		}
	}
	g.mu.Lock()
	for _, id := range removed {
		delete(g.objs, id)
	}
	for _, o := range parsed {
		g.objs[o.ID] = o
	}
	g.ready = true
	g.mu.Unlock()
}

// snapshot is the graph as pw-dump would print it, when the model can
// stand in for one.
func (g *graphModel) snapshot() ([]pwObject, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.ready || time.Since(g.changed) < freshAfterCommand {
		return nil, false
	}
	out := make([]pwObject, 0, len(g.objs))
	for _, o := range g.objs {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, true
}
