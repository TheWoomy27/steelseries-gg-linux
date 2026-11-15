package wldpms

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeKWin is a compositor speaking just enough Wayland for Watch.
type fakeKWin struct {
	t         *testing.T
	ln        *net.UnixListener
	path      string
	noManager bool

	mu        sync.Mutex
	conn      net.Conn
	registry  uint32
	outputs   map[uint32]*fakeOutput // by global name
	manager   uint32
	wlOutputs map[uint32]uint32 // wl_output object -> global name
	released  []uint32          // global names whose dpms object was released
}

type fakeOutput struct {
	mode      uint32
	supported bool
	dpms      uint32
}

const managerName = 100

func newFake(t *testing.T, modes ...uint32) *fakeKWin {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wayland-test")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeKWin{t: t, ln: ln, path: path, outputs: map[uint32]*fakeOutput{}, wlOutputs: map[uint32]uint32{}}
	for i, m := range modes {
		f.outputs[uint32(i+1)] = &fakeOutput{mode: m, supported: true}
	}
	t.Cleanup(func() {
		ln.Close()
		f.mu.Lock()
		if f.conn != nil {
			f.conn.Close()
		}
		f.mu.Unlock()
	})
	go f.serve()
	return f
}

func (f *fakeKWin) serve() {
	c, err := f.ln.Accept()
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conn = c
	f.mu.Unlock()
	w := &watcher{conn: c}
	for {
		obj, op, a, err := w.read()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.request(obj, op, a)
		f.mu.Unlock()
	}
}

// request handles a client request (f.mu held).
func (f *fakeKWin) request(obj uint32, op uint16, a *args) {
	switch {
	case obj == displayID && op == 1: // get_registry
		f.registry = a.u32()
		for name := range f.outputs {
			f.event(f.registry, 0, putStr(put32(nil, name), "wl_output"), 4)
		}
		f.event(f.registry, 0, putStr(put32(nil, 7), "wl_shm"), 1)
		if !f.noManager {
			f.event(f.registry, 0, putStr(put32(nil, managerName), "org_kde_kwin_dpms_manager"), 1)
		}
	case obj == displayID && op == 0: // sync
		f.event(a.u32(), 0, nil, 0)
	case obj == f.registry && op == 0: // bind
		name, iface, _, id := a.u32(), a.str(), a.u32(), a.u32()
		switch iface {
		case "wl_output":
			f.wlOutputs[id] = name
		case "org_kde_kwin_dpms_manager":
			f.manager = id
		}
	case obj == f.manager && op == 0: // get
		dpms, out := a.u32(), a.u32()
		o := f.outputs[f.wlOutputs[out]]
		if o == nil {
			f.t.Errorf("get for unknown wl_output %d", out)
			return
		}
		o.dpms = dpms
		s := uint32(0)
		if o.supported {
			s = 1
		}
		f.event(dpms, 0, put32(nil, s))
		f.event(dpms, 1, put32(nil, o.mode))
		f.event(dpms, 2, nil)
	default:
		for name, o := range f.outputs {
			if o.dpms == obj && op == 1 { // release
				f.released = append(f.released, name)
			}
		}
	}
}

// event sends an event; extra, if given, is appended as a version.
func (f *fakeKWin) event(obj uint32, op uint16, body []byte, version ...uint32) {
	for _, v := range version {
		body = put32(body, v)
	}
	w := &watcher{conn: f.conn}
	if err := w.send(obj, op, body); err != nil {
		f.t.Errorf("send: %v", err)
	}
}

func (f *fakeKWin) setMode(name, mode uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.outputs[name]
	o.mode = mode
	f.event(o.dpms, 1, put32(nil, mode))
	f.event(o.dpms, 2, nil)
}

func (f *fakeKWin) plug(name, mode uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outputs[name] = &fakeOutput{mode: mode, supported: true}
	f.event(f.registry, 0, putStr(put32(nil, name), "wl_output"), 4)
}

func (f *fakeKWin) unplug(name uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.event(f.registry, 1, put32(nil, name))
}

// start runs Watch against f, its changes on the returned channel.
func start(t *testing.T, f *fakeKWin) (<-chan bool, <-chan error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	changes := make(chan bool, 16)
	done := make(chan error, 1)
	go func() { done <- Watch(ctx, f.path, func(a bool) { changes <- a }) }()
	return changes, done, cancel
}

func expect(t *testing.T, changes <-chan bool, want bool) {
	t.Helper()
	select {
	case got := <-changes:
		if got != want {
			t.Fatalf("asleep = %v, want %v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no change (want asleep = %v)", want)
	}
}

func expectNone(t *testing.T, changes <-chan bool) {
	t.Helper()
	select {
	case got := <-changes:
		t.Fatalf("unexpected change: asleep = %v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAsleepOnlyWhenEveryMonitorIsOff(t *testing.T) {
	f := newFake(t, ModeOn, ModeOn)
	changes, _, _ := start(t, f)
	expect(t, changes, false)
	f.setMode(1, ModeOff)
	expectNone(t, changes)
	f.setMode(2, ModeStandby)
	expect(t, changes, true)
	f.setMode(1, ModeSuspend)
	expectNone(t, changes)
	f.setMode(2, ModeOn)
	expect(t, changes, false)
}

func TestStartsAsleep(t *testing.T) {
	f := newFake(t, ModeOff, ModeOff)
	changes, _, _ := start(t, f)
	expect(t, changes, true)
	expectNone(t, changes)
}

func TestHotplug(t *testing.T) {
	f := newFake(t, ModeOff)
	changes, _, _ := start(t, f)
	expect(t, changes, true)
	f.plug(5, ModeOn) // a monitor plugged in wakes things up
	expect(t, changes, false)
	f.unplug(5)
	expect(t, changes, true)
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	released := append([]uint32(nil), f.released...)
	f.mu.Unlock()
	if len(released) != 1 || released[0] != 5 {
		t.Errorf("released %v, want [5]", released)
	}
	f.unplug(1) // no monitors left: awake (nothing to say they sleep)
	expect(t, changes, false)
}

func TestUnsupportedOutputCountsAwake(t *testing.T) {
	f := newFake(t, ModeOff, ModeOff)
	f.mu.Lock()
	f.outputs[2].supported = false
	f.mu.Unlock()
	changes, _, _ := start(t, f)
	expect(t, changes, false)
}

func TestNotKWin(t *testing.T) {
	f := newFake(t, ModeOn)
	f.mu.Lock()
	f.noManager = true
	f.mu.Unlock()
	changes, done, _ := start(t, f)
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Watch = %v, want ErrUnsupported", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not return")
	}
	expectNone(t, changes)
}

func TestCancelAndDisconnect(t *testing.T) {
	f := newFake(t, ModeOn)
	changes, done, cancel := start(t, f)
	expect(t, changes, false)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Watch after cancel = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not return after cancel")
	}

	f = newFake(t, ModeOn)
	changes, done, _ = start(t, f)
	expect(t, changes, false)
	f.mu.Lock()
	f.conn.Close() // the compositor goes away
	f.mu.Unlock()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Watch after disconnect = nil, want an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not return after disconnect")
	}
}

func TestArgs(t *testing.T) {
	b := putStr(put32(nil, 9), "wl_output")
	b = put32(b, 4)
	a := &args{b: b}
	if n, s, v := a.u32(), a.str(), a.u32(); n != 9 || s != "wl_output" || v != 4 {
		t.Fatalf("got %d %q %d", n, s, v)
	}
	if binary.LittleEndian.Uint32(b[4:]) != 10 {
		t.Fatal("string length must count the NUL")
	}
}
