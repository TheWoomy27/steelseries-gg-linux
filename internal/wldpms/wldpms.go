// Package wldpms follows whether the monitors are powered through KWin's
// org_kde_kwin_dpms Wayland protocol (KDE Plasma): the compositor tells
// each output's power mode as it changes, so nothing is polled. It speaks
// just enough of the Wayland wire protocol for that.
package wldpms

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
)

// Power modes (org_kde_kwin_dpms.mode).
const (
	ModeOn      = 0
	ModeStandby = 1
	ModeSuspend = 2
	ModeOff     = 3
)

// ErrUnsupported is Watch's answer when the compositor has no
// org_kde_kwin_dpms (it is not KWin).
var ErrUnsupported = errors.New("wldpms: the compositor has no org_kde_kwin_dpms")

// SocketPath is the compositor's socket, from WAYLAND_DISPLAY.
func SocketPath() (string, error) {
	name := os.Getenv("WAYLAND_DISPLAY")
	if name == "" {
		return "", errors.New("no Wayland display")
	}
	if filepath.IsAbs(name) {
		return name, nil
	}
	return filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), name), nil
}

// output is a monitor and its power, as the compositor last told it.
type output struct {
	wlOutput  uint32
	outVers   uint32
	dpms      uint32
	supported bool
	mode      uint32
	pending   uint32
	known     bool // a done event came
}

// Watch connects to the compositor at path and calls changed with
// whether every monitor is off: once when it knows, then each time that
// changes. It returns when ctx ends (nil) or the connection fails.
func Watch(ctx context.Context, path string, changed func(asleep bool)) error {
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer conn.Close()
	w := &watcher{conn: conn, next: 2, globals: map[uint32]*output{}, byDPMS: map[uint32]*output{}}
	err = w.run(changed)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

const displayID = 1

type watcher struct {
	conn     io.ReadWriter
	next     uint32
	rbuf     []byte
	registry uint32
	manager  uint32
	globals  map[uint32]*output // by wl_registry name
	byDPMS   map[uint32]*output
	synced   bool
	reported int // -1 unknown, 0 awake, 1 asleep
}

func (w *watcher) run(changed func(bool)) error {
	w.reported = -1
	w.registry = w.newID()
	if err := w.send(displayID, 1, put32(nil, w.registry)); err != nil { // get_registry
		return err
	}
	// The registry announces every global before answering the sync; the
	// outputs' power comes in a second round trip.
	syncs := 0
	sync := w.newID()
	if err := w.send(displayID, 0, put32(nil, sync)); err != nil {
		return err
	}
	for {
		obj, op, a, err := w.read()
		if err != nil {
			return err
		}
		switch {
		case obj == displayID && op == 0: // error
			id, code := a.u32(), a.u32()
			return fmt.Errorf("wayland error on object %d (code %d): %s", id, code, a.str())
		case obj == sync && op == 0: // wl_callback.done
			syncs++
			if syncs == 1 {
				if w.manager == 0 {
					return ErrUnsupported
				}
				sync = w.newID()
				if err := w.send(displayID, 0, put32(nil, sync)); err != nil {
					return err
				}
				continue
			}
			w.synced = true
			sync = 0
		case obj == w.registry && op == 0: // global
			name, iface, version := a.u32(), a.str(), a.u32()
			if err := w.global(name, iface, version); err != nil {
				return err
			}
		case obj == w.registry && op == 1: // global_remove
			if err := w.remove(a.u32()); err != nil {
				return err
			}
		case w.byDPMS[obj] != nil:
			o := w.byDPMS[obj]
			switch op {
			case 0: // supported
				o.supported = a.u32() != 0
			case 1: // mode
				o.pending = a.u32()
			case 2: // done
				o.mode, o.known = o.pending, true
			}
		default:
			continue
		}
		if w.synced {
			w.report(changed)
		}
	}
}

// asleep reports whether every monitor is off (standby, suspended or
// off). Monitors that can't be powered down count as awake.
func (w *watcher) asleep() bool {
	if len(w.byDPMS) == 0 {
		return false
	}
	for _, o := range w.byDPMS {
		if !o.known || !o.supported || o.mode == ModeOn {
			return false
		}
	}
	return true
}

func (w *watcher) report(changed func(bool)) {
	now := 0
	if w.asleep() {
		now = 1
	}
	if now != w.reported {
		w.reported = now
		changed(now == 1)
	}
}

func (w *watcher) global(name uint32, iface string, version uint32) error {
	switch iface {
	case "org_kde_kwin_dpms_manager":
		w.manager = w.newID()
		if err := w.bind(name, iface, 1, w.manager); err != nil {
			return err
		}
		for _, o := range w.globals {
			if o.dpms != 0 {
				continue
			}
			if err := w.watchOutput(o); err != nil {
				return err
			}
		}
	case "wl_output":
		o := &output{wlOutput: w.newID(), outVers: min(version, 3)}
		w.globals[name] = o
		if err := w.bind(name, iface, o.outVers, o.wlOutput); err != nil {
			return err
		}
		if w.manager != 0 {
			return w.watchOutput(o)
		}
	}
	return nil
}

// watchOutput asks for an output's power.
func (w *watcher) watchOutput(o *output) error {
	o.dpms = w.newID()
	w.byDPMS[o.dpms] = o
	return w.send(w.manager, 0, put32(put32(nil, o.dpms), o.wlOutput)) // get
}

// remove forgets an unplugged output.
func (w *watcher) remove(name uint32) error {
	o := w.globals[name]
	if o == nil {
		return nil
	}
	delete(w.globals, name)
	if o.dpms != 0 {
		delete(w.byDPMS, o.dpms)
		if err := w.send(o.dpms, 1, nil); err != nil { // release
			return err
		}
	}
	if o.outVers >= 3 {
		return w.send(o.wlOutput, 0, nil) // release
	}
	return nil
}

func (w *watcher) bind(name uint32, iface string, version, id uint32) error {
	b := put32(nil, name)
	b = putStr(b, iface)
	b = put32(b, version)
	return w.send(w.registry, 0, put32(b, id))
}

// newID numbers a new object. Ids are never reused: the server accepts
// the next unused one, and a session makes few.
func (w *watcher) newID() uint32 {
	id := w.next
	w.next++
	return id
}

// send writes a request.
func (w *watcher) send(obj uint32, op uint16, body []byte) error {
	msg := make([]byte, 8+len(body))
	binary.LittleEndian.PutUint32(msg[0:], obj)
	binary.LittleEndian.PutUint32(msg[4:], uint32(len(msg))<<16|uint32(op))
	copy(msg[8:], body)
	_, err := w.conn.Write(msg)
	return err
}

// read returns the next event.
func (w *watcher) read() (uint32, uint16, *args, error) {
	buf := make([]byte, 4096)
	for {
		if len(w.rbuf) >= 8 {
			size := int(binary.LittleEndian.Uint32(w.rbuf[4:]) >> 16)
			if size < 8 {
				return 0, 0, nil, errors.New("wldpms: bad message")
			}
			if len(w.rbuf) >= size {
				obj := binary.LittleEndian.Uint32(w.rbuf[0:])
				op := uint16(binary.LittleEndian.Uint32(w.rbuf[4:]))
				a := &args{b: append([]byte(nil), w.rbuf[8:size]...)}
				w.rbuf = w.rbuf[size:]
				return obj, op, a, nil
			}
		}
		n, err := w.conn.Read(buf)
		if n > 0 {
			w.rbuf = append(w.rbuf, buf[:n]...)
			continue
		}
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return 0, 0, nil, err
	}
}

func put32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }

func putStr(b []byte, s string) []byte {
	b = put32(b, uint32(len(s)+1))
	b = append(b, s...)
	b = append(b, 0)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// args reads an event's arguments.
type args struct{ b []byte }

func (a *args) u32() uint32 {
	if len(a.b) < 4 {
		return 0
	}
	v := binary.LittleEndian.Uint32(a.b)
	a.b = a.b[4:]
	return v
}

func (a *args) str() string {
	n := int(a.u32())
	if n == 0 || n > len(a.b) {
		return ""
	}
	s := string(a.b[:n-1])
	a.b = a.b[min((n+3)&^3, len(a.b)):]
	return s
}
