package engine

import (
	"log/slog"
	"sync"

	"ggl/internal/devicert"
	"ggl/internal/hid"
)

// PrismSync (GG's Prism lighting) drives the lights of supporting devices
// frame by frame. The prism service renders the frames; Engine tells it
// which devices can take them (from their specs) and writes the frames.

// PrismDevice is a connected device PrismSync can light.
type PrismDevice struct {
	ID        int // the device's Engine id
	ProductID uint32
	Type      int
	Name      string
	FullName  string
	Data      *devicert.PrismData
}

type prismState struct {
	mu      sync.Mutex
	data    map[uint32]*devicert.PrismData // by physical product
	changed []func()
}

// prismDecl is the physical interface of a device that takes PrismSync
// frames.
func (l *liveDevice) prismDecl() *devicert.Decl {
	for _, d := range l.decls() {
		if d.SupportsPrismSync {
			return d
		}
	}
	return nil
}

// PrismSyncDevices lists the connected devices PrismSync can light.
func (e *Engine) PrismSyncDevices() []PrismDevice {
	e.mu.Lock()
	lives := make([]*liveDevice, 0, len(e.live))
	for _, l := range e.live {
		lives = append(lives, l)
	}
	e.mu.Unlock()
	var out []PrismDevice
	for _, l := range lives {
		l.mu.Lock()
		ready := l.ready
		l.mu.Unlock()
		d := l.prismDecl()
		if !ready || d == nil {
			continue
		}
		p := e.prismData(d)
		// PrismSync V2 lights only the devices SteelSeries whitelisted for
		// it, as the Windows Engine does by default.
		if p == nil || !p.Whitelisted || len(p.Zones) == 0 {
			continue
		}
		out = append(out, PrismDevice{ID: l.dbID, ProductID: d.ID, Type: l.cat.Type, Name: l.cat.Name, FullName: l.cat.FullName, Data: p})
	}
	return out
}

// prismData reads (once per connection) how a device takes frames.
func (e *Engine) prismData(d *devicert.Decl) *devicert.PrismData {
	ps := &e.prism
	ps.mu.Lock()
	p, ok := ps.data[d.ID]
	ps.mu.Unlock()
	if ok {
		return p
	}
	p, err := d.PrismData()
	if err != nil {
		slog.Warn("prism sync data", "spec", d.Name, "err", err)
		p = nil
	}
	ps.mu.Lock()
	if ps.data == nil {
		ps.data = map[uint32]*devicert.PrismData{}
	}
	ps.data[d.ID] = p
	ps.mu.Unlock()
	return p
}

// PrismWrite sends a frame to a device. Frames wait while a configuration
// is being written, as the Windows Engine pauses PrismSync then.
func (e *Engine) PrismWrite(id int, writes []devicert.PrismWrite) error {
	l := e.liveByID(id)
	if l == nil {
		return nil
	}
	d := l.prismDecl()
	if d == nil {
		return nil
	}
	e.mu.Lock()
	busy := e.deploying[d.ID] > 0
	e.mu.Unlock()
	l.mu.Lock()
	away := l.linkKnown && !l.linkUp
	l.mu.Unlock()
	if busy || away {
		// A wireless device asleep takes frames again once it wakes.
		return nil
	}
	return d.WritePrism(writes)
}

// PrismRelease gives a device its own lighting back when PrismSync stops
// lighting it: the configuration GG manages is written again, then the
// spec resets the lighting PrismSync overrode.
func (e *Engine) PrismRelease(id int) {
	l := e.liveByID(id)
	if l == nil {
		return
	}
	if cfg, err := e.activeConfig(l.dbID); err == nil && e.managed(l.dbID) {
		if err := e.deploy(l, cfg.settingsMap(), true, false); err != nil {
			slog.Warn("redeploy after prism sync", "device", l.cat.Name, "err", err)
		}
	}
	if d := l.prismDecl(); d != nil && d.HasFunction("prism-sync-post-redeploy") {
		if _, err := d.Call("prism-sync-post-redeploy"); err != nil {
			slog.Debug("prism-sync-post-redeploy", "spec", d.Name, "err", err)
		}
	}
}

// OnPrismDevicesChanged runs fn when devices PrismSync may light connect
// or disconnect.
func (e *Engine) OnPrismDevicesChanged(fn func()) {
	e.prism.mu.Lock()
	e.prism.changed = append(e.prism.changed, fn)
	e.prism.mu.Unlock()
}

func (e *Engine) prismDevicesChanged(gone []uint32) {
	ps := &e.prism
	ps.mu.Lock()
	for _, id := range gone {
		delete(ps.data, id)
	}
	fns := ps.changed
	ps.mu.Unlock()
	for _, fn := range fns {
		go fn()
	}
	go e.rewatchPrismInput()
	go e.rewatchSoft()
}

// PrismInput is input PrismSync's reactive and idle layers follow: the
// keys a SteelSeries keyboard reports newly pressed (HID usages), or
// none for other activity (a mouse moving).
type PrismInput struct {
	DeviceID int
	Pressed  []uint8
}

type prismInputState struct {
	mu     sync.Mutex
	fn     func(PrismInput)
	remove []func()
}

// WatchPrismInput follows the keyboards' and mice's input for PrismSync
// (fn nil stops). Only SteelSeries devices are watched, and only while
// PrismSync asks: keys go straight to the lighting and nowhere else.
func (e *Engine) WatchPrismInput(fn func(PrismInput)) {
	st := &e.prismInput
	st.mu.Lock()
	st.fn = fn
	st.mu.Unlock()
	e.rewatchPrismInput()
}

// rewatchPrismInput (re)subscribes to the input of connected devices.
func (e *Engine) rewatchPrismInput() {
	st := &e.prismInput
	st.mu.Lock()
	for _, r := range st.remove {
		r()
	}
	st.remove = nil
	fn := st.fn
	st.mu.Unlock()
	if fn == nil {
		return
	}
	e.mu.Lock()
	lives := make([]*liveDevice, 0, len(e.live))
	for _, l := range e.live {
		lives = append(lives, l)
	}
	e.mu.Unlock()
	var removers []func()
	for _, l := range lives {
		for _, d := range l.decls() {
			for _, node := range e.hid.Devices(uint16(d.ID & 0xffff)) {
				desc := node.Desc
				mouseIDs := map[uint8]bool{}
				for _, c := range desc.Collections {
					if c.UsagePage == 1 && c.Usage == 2 {
						mouseIDs[0] = len(desc.Collections) == 1
						for _, id := range c.ReportIDs {
							mouseIDs[id] = true
						}
					}
				}
				if len(desc.Keys) == 0 && len(mouseIDs) == 0 {
					continue
				}
				id := l.dbID
				var held map[uint8]bool
				removers = append(removers, node.Listen(func(r []byte) {
					if len(r) == 0 {
						return
					}
					if mouseIDs[r[0]] {
						fn(PrismInput{DeviceID: id})
						return
					}
					if len(desc.Keys) == 0 {
						return
					}
					keys := desc.PressedKeys(r)
					now := map[uint8]bool{}
					var pressed []uint8
					for _, k := range keys {
						now[k] = true
						if !held[k] {
							pressed = append(pressed, k)
						}
					}
					if keyReport(desc, r[0]) {
						held = now
						fn(PrismInput{DeviceID: id, Pressed: pressed})
					}
				}))
			}
		}
	}
	st.mu.Lock()
	st.remove = removers
	st.mu.Unlock()
}

// keyReport reports whether a report ID carries keys.
func keyReport(desc hid.Descriptor, id uint8) bool {
	for _, f := range desc.Keys {
		if f.ReportID == id {
			return true
		}
	}
	return false
}
