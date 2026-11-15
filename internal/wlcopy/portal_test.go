package wlcopy

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	"ggl/internal/dbustest"
)

// fakeScreenCast is a desktop portal with ScreenCast, answering as GNOME's
// does; cancel makes the user decline in the dialog.
type fakeScreenCast struct {
	conn   *dbus.Conn
	cancel bool

	mu       sync.Mutex
	restored string // the restore token SelectSources got
	persist  uint32
	closed   bool
}

func requestPath(sender dbus.Sender, opts map[string]dbus.Variant) dbus.ObjectPath {
	tok, _ := opts["handle_token"].Value().(string)
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + escapeSender(string(sender)) + "/" + tok)
}

func (f *fakeScreenCast) respond(sender dbus.Sender, opts map[string]dbus.Variant, code uint32, res map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	req := requestPath(sender, opts)
	f.conn.Emit(req, portalRequest+".Response", code, res)
	return req, nil
}

type castProps struct{}

func (castProps) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	switch name {
	case "version":
		return dbus.MakeVariant(uint32(5)), nil
	case "AvailableCursorModes":
		return dbus.MakeVariant(uint32(3)), nil
	}
	return dbus.Variant{}, dbus.MakeFailedError(errors.New("no such property"))
}

type castMethods struct{ f *fakeScreenCast }

func (m castMethods) CreateSession(sender dbus.Sender, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	tok, _ := opts["session_handle_token"].Value().(string)
	sess := "/org/freedesktop/portal/desktop/session/" + escapeSender(string(sender)) + "/" + tok
	m.f.conn.Export(castSession{m.f}, dbus.ObjectPath(sess), portalSession)
	return m.f.respond(sender, opts, 0, map[string]dbus.Variant{"session_handle": dbus.MakeVariant(sess)})
}

func (m castMethods) SelectSources(sender dbus.Sender, sess dbus.ObjectPath, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	m.f.mu.Lock()
	m.f.restored, _ = opts["restore_token"].Value().(string)
	m.f.persist, _ = opts["persist_mode"].Value().(uint32)
	cancel := m.f.cancel
	m.f.mu.Unlock()
	if cancel {
		return m.f.respond(sender, opts, 1, map[string]dbus.Variant{})
	}
	return m.f.respond(sender, opts, 0, map[string]dbus.Variant{})
}

func (m castMethods) Start(sender dbus.Sender, sess dbus.ObjectPath, parent string, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	type stream struct {
		Node  uint32
		Props map[string]dbus.Variant
	}
	streams := []stream{{42, map[string]dbus.Variant{"size": dbus.MakeVariant(struct{ W, H int32 }{2560, 1440})}}}
	return m.f.respond(sender, opts, 0, map[string]dbus.Variant{
		"streams":       dbus.MakeVariant(streams),
		"restore_token": dbus.MakeVariant("token-2"),
	})
}

func (m castMethods) OpenPipeWireRemote(sess dbus.ObjectPath, opts map[string]dbus.Variant) (dbus.UnixFD, *dbus.Error) {
	r, w, err := os.Pipe()
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}
	w.Close()
	return dbus.UnixFD(r.Fd()), nil
}

type castSession struct{ f *fakeScreenCast }

func (s castSession) Close() *dbus.Error {
	s.f.mu.Lock()
	s.f.closed = true
	s.f.mu.Unlock()
	return nil
}

func startFakeScreenCast(t *testing.T) (*fakeScreenCast, string) {
	addr := dbustest.Bus(t)
	srv, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	f := &fakeScreenCast{conn: srv}
	srv.Export(castProps{}, portalPath, "org.freedesktop.DBus.Properties")
	srv.Export(castMethods{f}, portalPath, screenCastIf)
	if _, err := srv.RequestName(portalService, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	return f, addr
}

// TestPortalCapture shares a monitor through a fake portal, with
// GStreamer's test picture (solid red) for the PipeWire stream.
func TestPortalCapture(t *testing.T) {
	if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
		t.Skip("this test needs gst-launch-1.0 (GStreamer tools and base plugins)")
	}
	if exec.Command("gst-inspect-1.0", "--exists", "videotestsrc", "videoscale", "videoconvert", "fdsink", "pipewiresrc").Run() != nil {
		t.Skip("this test needs GStreamer's base and PipeWire plugins")
	}
	orig := portalSource
	defer func() { portalSource = orig }()
	portalSource = func(node uint32) []string {
		if node != 42 {
			t.Errorf("node %d, want 42", node)
		}
		return []string{"videotestsrc", "pattern=red", "num-buffers=3", "!", "video/x-raw,format=BGRx,width=2560,height=1440,framerate=30/1"}
	}
	f, addr := startFakeScreenCast(t)
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("token-1"), 0o600)
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	p, err := openPortal(conn, token)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	restored, persist := f.restored, f.persist
	f.mu.Unlock()
	if restored != "token-1" || persist != persistAlways {
		t.Errorf("SelectSources got token %q, persist %d", restored, persist)
	}
	if b, _ := os.ReadFile(token); string(b) != "token-2" {
		t.Errorf("token kept %q, want the new one", b)
	}
	fr, err := p.CaptureChanged()
	for i := 0; i < 4 && errors.Is(err, ErrUnchanged); i++ {
		fr, err = p.CaptureChanged()
	}
	if err != nil {
		t.Fatal(err)
	}
	if fr.Width != 320 || fr.Height != 180 || len(fr.Pix) != 320*180*4 {
		t.Fatalf("frame %dx%d, %d bytes", fr.Width, fr.Height, len(fr.Pix))
	}
	if px := fr.Pix[(90*320+160)*4:]; px[0] != 255 || px[1] != 0 || px[2] != 0 {
		t.Errorf("middle pixel %v, want red", px[:4])
	}
	p.Close()
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if !closed {
		t.Error("the portal session was left open")
	}
}

// When the user declines in the portal's dialog, capture fails with
// ErrNotShared, so Reflect doesn't ask again at once.
func TestPortalDeclined(t *testing.T) {
	if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
		t.Skip("this test needs gst-launch-1.0")
	}
	if exec.Command("gst-inspect-1.0", "--exists", "pipewiresrc").Run() != nil {
		t.Skip("this test needs GStreamer's PipeWire plugin")
	}
	f, addr := startFakeScreenCast(t)
	f.cancel = true
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = openPortal(conn, filepath.Join(t.TempDir(), "token"))
	if !errors.Is(err, ErrNotShared) {
		t.Fatalf("declined: %v", err)
	}
	if !strings.Contains(ErrNotShared.Error(), "shared") {
		t.Error(ErrNotShared)
	}
}
