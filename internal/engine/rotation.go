package engine

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"ggl/internal/mousefilter"
)

// Mouse rotation: GG's Windows driver turns the motion of the mice whose
// rotation setting says "GG required" (the Sensei and Rival Pro, the
// Aerox 3 Wireless Gen 2). Their specs send the angle to the driver
// (MOUSEROTATION); ggd turns the motion itself with package mousefilter.

type rotationState struct {
	mu      sync.Mutex
	want    map[uint32]rotationWant // by the physical device the spec wrote to
	running map[uint32]*rotationFilter
	kick    chan struct{}
}

type rotationWant struct {
	on      bool
	degrees float64
}

type rotationFilter struct {
	filter *mousefilter.Filter
	nodes  []string
	err    string // the last error, logged once
}

// MouseRotation implements devicert.DriverHooks. It is called from a
// spec's write, so the work is left to runRotation.
func (e *Engine) MouseRotation(pid uint32, on bool, degrees float64) {
	r := &e.rotation
	r.mu.Lock()
	if r.want == nil {
		r.want = map[uint32]rotationWant{}
	}
	r.want[pid] = rotationWant{on, degrees}
	r.mu.Unlock()
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// runRotation keeps the filters matching the rotation settings and the
// connected mice.
func (e *Engine) runRotation(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	defer e.syncRotation(true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.rotation.kick:
		case <-t.C:
		}
		e.syncRotation(false)
	}
}

func (e *Engine) syncRotation(stop bool) {
	r := &e.rotation
	r.mu.Lock()
	want := make(map[uint32]rotationWant, len(r.want))
	for pid, w := range r.want {
		want[pid] = w
	}
	r.mu.Unlock()
	if r.running == nil {
		r.running = map[uint32]*rotationFilter{}
	}
	for pid, w := range want {
		rf := r.running[pid]
		var nodes []string
		var name string
		if l := e.liveByProduct(pid); l != nil && w.on && w.degrees != 0 && !stop {
			name = l.cat.Name
			for _, d := range l.decls() {
				nodes = append(nodes, mousefilter.Nodes(uint16(d.ID>>16), uint16(d.ID))...)
			}
			slices.Sort(nodes)
		}
		if rf != nil && rf.filter != nil && slices.Equal(rf.nodes, nodes) && nodesExist(nodes) {
			rf.filter.SetAngle(w.degrees)
			continue
		}
		if rf != nil && rf.filter != nil {
			rf.filter.Close()
			rf.filter = nil
			slog.Info("mouse rotation off", "device", pid)
		}
		if len(nodes) == 0 {
			if rf != nil && name == "" {
				delete(r.running, pid)
			}
			continue
		}
		if rf == nil {
			rf = &rotationFilter{}
			r.running[pid] = rf
		}
		f, err := mousefilter.Start(nodes, w.degrees)
		if err != nil {
			if err.Error() != rf.err {
				slog.Warn("mouse rotation: can't take the mouse's input (run setup.sh again for its udev rule)", "device", name, "err", err)
			}
			rf.err, rf.nodes = err.Error(), nil
			continue
		}
		rf.filter, rf.nodes, rf.err = f, nodes, ""
		slog.Info("mouse rotation", "device", name, "degrees", w.degrees, "inputs", nodes)
	}
}

func nodesExist(nodes []string) bool {
	for _, n := range nodes {
		if _, err := os.Stat(n); err != nil {
			return false
		}
	}
	return true
}
