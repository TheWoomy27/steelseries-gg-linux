package wlcopy

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

// Desktops whose compositor has neither capture protocol (GNOME, and any
// other with a desktop portal) share a monitor through the portal's
// ScreenCast: the user picks the monitor in the desktop's dialog once,
// and the portal's restore token brings the same one back next time
// without asking. The monitor comes as a PipeWire stream, which
// GStreamer's pipewiresrc reads over the connection the portal opened,
// as for KDE.

// ErrUsePortal is Open's error where only the desktop portal can capture.
var ErrUsePortal = errors.New("the compositor has neither wlr-screencopy nor KDE's screencast protocol")

// ErrNotShared is the error when the user declined to share a monitor, or
// stopped sharing it. It is not asked for again until capture starts anew.
var ErrNotShared = errors.New("no monitor shared with the desktop portal")

const (
	portalService  = "org.freedesktop.portal.Desktop"
	portalPath     = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	screenCastIf   = "org.freedesktop.portal.ScreenCast"
	portalRequest  = "org.freedesktop.portal.Request"
	portalSession  = "org.freedesktop.portal.Session"
	sourceMonitor  = 1
	cursorHidden   = 1
	persistAlways  = 2
	portalTokenTag = "gg_linux_reflect"
)

// Portal is a monitor shared through the desktop portal.
type Portal struct {
	conn    *dbus.Conn
	session dbus.ObjectPath
	w, h    int
	gst     *exec.Cmd
	frames  chan []byte
	done    chan error
	got     bool
	last    []byte

	mu     sync.Mutex
	closed bool // the user stopped sharing
}

// portalSource is the GStreamer source for a portal stream: pipewiresrc on
// the portal's connection, which the child gets as fd 3 (tests replace it).
var portalSource = func(node uint32) []string {
	return []string{"pipewiresrc", "fd=3", "path=" + strconv.FormatUint(uint64(node), 10)}
}

// OpenPortal shares a monitor through the desktop portal, with the restore
// token kept in tokenFile, and starts reading it.
func OpenPortal(tokenFile string) (*Portal, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	p, err := openPortal(conn, tokenFile)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return p, nil
}

func openPortal(conn *dbus.Conn, tokenFile string) (*Portal, error) {
	if err := gstCheck(); err != nil {
		return nil, err
	}
	obj := conn.Object(portalService, portalPath)
	v, err := obj.GetProperty(screenCastIf + ".version")
	if err != nil {
		return nil, fmt.Errorf("no desktop portal for screen sharing: %w", err)
	}
	version, _ := v.Value().(uint32)
	p := &Portal{conn: conn}
	req := &portalRequests{conn: conn}
	if err := req.watch(); err != nil {
		return nil, err
	}
	res, err := req.call(obj, screenCastIf+".CreateSession", map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(portalTokenTag),
	})
	if err != nil {
		return nil, fmt.Errorf("screen sharing session: %w", err)
	}
	session, _ := res["session_handle"].Value().(string)
	p.session = dbus.ObjectPath(session)
	if !p.session.IsValid() {
		return nil, errors.New("the portal gave no screen sharing session")
	}
	opts := map[string]dbus.Variant{
		"types":    dbus.MakeVariant(uint32(sourceMonitor)),
		"multiple": dbus.MakeVariant(false),
	}
	if c, err := obj.GetProperty(screenCastIf + ".AvailableCursorModes"); err == nil {
		if modes, _ := c.Value().(uint32); modes&cursorHidden != 0 {
			opts["cursor_mode"] = dbus.MakeVariant(uint32(cursorHidden))
		}
	}
	if version >= 4 {
		opts["persist_mode"] = dbus.MakeVariant(uint32(persistAlways))
		if b, err := os.ReadFile(tokenFile); err == nil && len(b) > 0 {
			opts["restore_token"] = dbus.MakeVariant(string(b))
		}
	}
	if _, err := req.call(obj, screenCastIf+".SelectSources", opts, p.session); err != nil {
		p.closeSession()
		return nil, err
	}
	res, err = req.call(obj, screenCastIf+".Start", map[string]dbus.Variant{}, p.session, "")
	if err != nil {
		p.closeSession()
		return nil, err
	}
	if tok, _ := res["restore_token"].Value().(string); tok != "" {
		os.MkdirAll(filepath.Dir(tokenFile), 0o700)
		os.WriteFile(tokenFile, []byte(tok), 0o600)
	}
	var streams []struct {
		Node  uint32
		Props map[string]dbus.Variant
	}
	if s, ok := res["streams"]; !ok || dbus.Store([]any{s.Value()}, &streams) != nil || len(streams) == 0 {
		p.closeSession()
		return nil, errors.New("the portal shared no stream")
	}
	ow, oh := 0, 0
	if size, ok := streams[0].Props["size"]; ok {
		var wh struct{ W, H int32 }
		if dbus.Store([]any{size.Value()}, &wh) == nil {
			ow, oh = int(wh.W), int(wh.H)
		}
	}
	p.w, p.h = scaledSize(ow, oh, StreamHeight)
	var fd dbus.UnixFD
	if err := obj.Call(screenCastIf+".OpenPipeWireRemote", 0, p.session, map[string]dbus.Variant{}).Store(&fd); err != nil {
		p.closeSession()
		return nil, fmt.Errorf("the portal's PipeWire connection: %w", err)
	}
	remote := os.NewFile(uintptr(fd), "pipewire-remote")
	defer remote.Close() // the child has its own copy
	// The user can stop sharing from the desktop (GNOME's indicator).
	conn.AddMatchSignal(dbus.WithMatchObjectPath(p.session), dbus.WithMatchInterface(portalSession), dbus.WithMatchMember("Closed"))
	req.mu.Lock()
	req.onClosed = func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
	}
	req.mu.Unlock()
	if err := p.start(streams[0].Node, remote); err != nil {
		p.closeSession()
		return nil, err
	}
	return p, nil
}

// scaledSize is the size frames are scaled to: height pixels high, the
// monitor's shape (16:9 when the portal doesn't say).
func scaledSize(w, h, height int) (int, int) {
	if w <= 0 || h <= 0 {
		return height * 16 / 9, height
	}
	return max(int(math.Round(float64(w)*float64(height)/float64(h))), 1), height
}

func (p *Portal) start(node uint32, remote *os.File) error {
	cmd := exec.Command("gst-launch-1.0", gstArgs(portalSource(node), p.w, p.h)...)
	cmd.ExtraFiles = []*os.File{remote}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	stderr := &tailWriter{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	p.gst = cmd
	p.frames, p.done = make(chan []byte, 1), make(chan error, 1)
	go readFrames(stdout, p.w*p.h*4, p.frames, p.done, func() error {
		cmd.Wait()
		return fmt.Errorf("gst-launch-1.0 ended: %s", stderr.String())
	})
	return nil
}

// CaptureChanged is the next frame of the shared monitor, ErrUnchanged
// when none came within CaptureTimeout, ErrNotShared once the user
// stopped sharing.
func (p *Portal) CaptureChanged() (*Frame, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, ErrNotShared
	}
	timeout := CaptureTimeout
	if !p.got {
		timeout += 2 * time.Second // GStreamer starting
	}
	select {
	case pix := <-p.frames:
		p.got, p.last = true, pix
		return &Frame{Width: p.w, Height: p.h, Stride: p.w * 4, Format: FormatXBGR8888, Pix: pix}, nil
	case err := <-p.done:
		p.mu.Lock()
		closed := p.closed
		p.mu.Unlock()
		if closed {
			return nil, ErrNotShared
		}
		return nil, err
	case <-time.After(timeout):
		return nil, ErrUnchanged
	}
}

// Close stops sharing.
func (p *Portal) Close() {
	if p.gst != nil {
		p.gst.Process.Kill()
		p.gst = nil
	}
	p.closeSession()
	p.conn.Close()
}

func (p *Portal) closeSession() {
	if p.session != "" {
		p.conn.Object(portalService, p.session).Call(portalSession+".Close", 0)
		p.session = ""
	}
}

// portalRequests makes portal requests and waits for their answers, which
// come as Response signals from the portal itself.
type portalRequests struct {
	conn     *dbus.Conn
	n        int
	mu       sync.Mutex
	waiting  map[dbus.ObjectPath]chan *dbus.Signal
	owner    string
	onClosed func()
}

func (r *portalRequests) watch() error {
	if err := r.conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, portalService).Store(&r.owner); err != nil {
		return fmt.Errorf("no desktop portal: %w", err)
	}
	r.waiting = map[dbus.ObjectPath]chan *dbus.Signal{}
	if err := r.conn.AddMatchSignal(dbus.WithMatchInterface(portalRequest), dbus.WithMatchMember("Response")); err != nil {
		return err
	}
	ch := make(chan *dbus.Signal, 8)
	r.conn.Signal(ch)
	go func() {
		for sig := range ch {
			if sig.Sender != r.owner {
				continue
			}
			switch sig.Name {
			case portalRequest + ".Response":
				r.mu.Lock()
				w := r.waiting[sig.Path]
				r.mu.Unlock()
				if w != nil {
					select {
					case w <- sig:
					default:
					}
				}
			case portalSession + ".Closed":
				r.mu.Lock()
				f := r.onClosed
				r.mu.Unlock()
				if f != nil {
					f()
				}
			}
		}
	}()
	return nil
}

// call makes a request: args come before the options, which get a handle
// token. It waits for the answer (the user may be asked first).
func (r *portalRequests) call(obj dbus.BusObject, method string, opts map[string]dbus.Variant, args ...any) (map[string]dbus.Variant, error) {
	r.mu.Lock()
	r.n++
	token := fmt.Sprintf("%s_%d", portalTokenTag, r.n)
	r.mu.Unlock()
	opts["handle_token"] = dbus.MakeVariant(token)
	// The request's path is known before the call, so its answer can't be
	// missed: /org/freedesktop/portal/desktop/request/SENDER/TOKEN.
	sender := r.conn.Names()[0]
	path := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + escapeSender(sender) + "/" + token)
	ch := make(chan *dbus.Signal, 1)
	r.mu.Lock()
	r.waiting[path] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.waiting, path)
		r.mu.Unlock()
	}()
	var handle dbus.ObjectPath
	if err := obj.Call(method, 0, append(args, opts)...).Store(&handle); err != nil {
		return nil, err
	}
	if handle != path {
		r.mu.Lock()
		r.waiting[handle] = ch
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			delete(r.waiting, handle)
			r.mu.Unlock()
		}()
	}
	select {
	case sig := <-ch:
		var code uint32
		var res map[string]dbus.Variant
		if dbus.Store(sig.Body, &code, &res) != nil {
			return nil, errors.New("unreadable portal answer")
		}
		switch code {
		case 0:
			return res, nil
		case 1:
			return nil, ErrNotShared
		}
		return nil, fmt.Errorf("the portal refused (%d)", code)
	case <-time.After(2 * time.Minute): // the user may take a while
		return nil, errors.New("no answer from the portal")
	}
}

func escapeSender(s string) string {
	b := []byte(s)
	if len(b) > 0 && b[0] == ':' {
		b = b[1:]
	}
	for i, c := range b {
		if c == '.' {
			b[i] = '_'
		}
	}
	return string(b)
}
