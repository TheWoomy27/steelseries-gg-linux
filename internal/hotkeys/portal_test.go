package hotkeys

import (
	"bufio"
	"context"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestPortalTrigger(t *testing.T) {
	for spec, want := range map[string]string{
		"ALT,S":        "ALT+s",
		"SHIFT ALT,M":  "SHIFT+ALT+m",
		"CTRL SUPER,0": "CTRL+LOGO+0",
		",F10":         "F10",
		"CTRL,Prior":   "CTRL+Prior",
		"ALT,KP_1":     "ALT+KP_1",
	} {
		if got := portalTrigger(spec); got != want {
			t.Errorf("portalTrigger(%q) = %q, want %q", spec, got, want)
		}
	}
}

type shortcutArg struct {
	ID      string
	Options map[string]dbus.Variant
}

// fakePortal is a desktop portal with GlobalShortcuts, answering as KDE's
// does.
type fakePortal struct {
	conn  *dbus.Conn
	event chan string // each call, as it comes

	mu    sync.Mutex
	binds []shortcutArg   // the last BindShortcuts
	sess  dbus.ObjectPath // the last session made
}

func (f *fakePortal) record(s string) { f.event <- s }

func escapeSender(s dbus.Sender) string {
	return strings.ReplaceAll(strings.TrimPrefix(string(s), ":"), ".", "_")
}

func requestPath(sender dbus.Sender, opts map[string]dbus.Variant) dbus.ObjectPath {
	tok, _ := opts["handle_token"].Value().(string)
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + escapeSender(sender) + "/" + tok)
}

type registry struct{ f *fakePortal }

func (r registry) Register(app string, opts map[string]dbus.Variant) *dbus.Error {
	r.f.record("Register " + app)
	return nil
}

type props struct{ f *fakePortal }

func (p props) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	if iface == shortcutsIface && name == "version" {
		p.f.record("version")
		return dbus.MakeVariant(uint32(2)), nil
	}
	return dbus.Variant{}, dbus.MakeFailedError(nil)
}

type shortcuts struct{ f *fakePortal }

func (s shortcuts) CreateSession(sender dbus.Sender, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	tok, _ := opts["session_handle_token"].Value().(string)
	sess := dbus.ObjectPath("/org/freedesktop/portal/desktop/session/" + escapeSender(sender) + "/" + tok)
	s.f.conn.Export(session{s.f}, sess, sessionIface)
	s.f.mu.Lock()
	s.f.sess = sess
	s.f.mu.Unlock()
	req := requestPath(sender, opts)
	s.f.conn.Emit(req, requestIface+".Response", uint32(0), map[string]dbus.Variant{"session_handle": dbus.MakeVariant(string(sess))})
	s.f.record("CreateSession " + tok)
	return req, nil
}

func (s shortcuts) BindShortcuts(sender dbus.Sender, sess dbus.ObjectPath, list []shortcutArg, parent string, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	var ids []string
	var out []shortcutArg
	for _, sc := range list {
		trig, _ := sc.Options["preferred_trigger"].Value().(string)
		ids = append(ids, sc.ID+"="+trig)
		out = append(out, shortcutArg{sc.ID, map[string]dbus.Variant{"trigger_description": dbus.MakeVariant(trig)}})
	}
	sort.Strings(ids)
	s.f.mu.Lock()
	s.f.binds = list
	s.f.mu.Unlock()
	req := requestPath(sender, opts)
	s.f.conn.Emit(req, requestIface+".Response", uint32(0), map[string]dbus.Variant{"shortcuts": dbus.MakeVariant(out)})
	s.f.record("Bind " + strings.Join(ids, " "))
	return req, nil
}

type session struct{ f *fakePortal }

func (s session) Close() *dbus.Error {
	s.f.record("Close")
	return nil
}

// privateBus starts a session bus of the test's own (dbus-daemon, from
// the dbus package, is needed).
func privateBus(t *testing.T) string {
	t.Helper()
	daemon := exec.Command("dbus-daemon", "--session", "--nofork", "--print-address=1")
	out, err := daemon.StdoutPipe()
	if err == nil {
		err = daemon.Start()
	}
	if err != nil {
		t.Fatal("dbus-daemon:", err)
	}
	t.Cleanup(func() { daemon.Process.Kill(); daemon.Wait() })
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(addr)
}

// TestPortal binds shortcuts through a fake GlobalShortcuts portal on a
// private bus, presses them and changes them.
func TestPortal(t *testing.T) {
	addr := privateBus(t)
	srv, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	f := &fakePortal{conn: srv, event: make(chan string, 32)}
	srv.Export(registry{f}, portalPath, registryIface)
	srv.Export(props{f}, portalPath, "org.freedesktop.DBus.Properties")
	srv.Export(shortcuts{f}, portalPath, shortcutsIface)
	if _, err := srv.RequestName(portalService, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}

	cli, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	ran := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := newPortal(ctx, cli, func(c string) { ran <- c })
	if err != nil {
		t.Fatal(err)
	}
	expect := func(want string) {
		t.Helper()
		select {
		case got := <-f.event:
			if got != want {
				t.Fatalf("portal call %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no portal call, want %q", want)
		}
	}
	expectRun := func(want string) {
		t.Helper()
		select {
		case got := <-ran:
			if got != want {
				t.Errorf("ran %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("pressing the shortcut ran nothing, want %q", want)
		}
	}
	expectNoRun := func(why string) {
		t.Helper()
		select {
		case got := <-ran:
			t.Errorf("ran %q %s", got, why)
		case <-time.After(200 * time.Millisecond):
		}
	}
	// The app id is registered before anything else is asked.
	expect("Register " + AppID)
	expect("version")

	p.Set(map[string]Bind{
		"ALT,S":   {ID: "moments-clip", Command: "ggctl moments clip", Description: "Moments: save a clip"},
		"CTRL,F1": {ID: "quickset-profile-1", Command: "ggctl profile 1", Description: "QuickSet: switch to profile 1"},
	})
	expect("CreateSession " + sessionToken)
	expect("Bind moments-clip=ALT+s quickset-profile-1=CTRL+F1")
	f.mu.Lock()
	sess := f.sess
	desc, _ := f.binds[0].Options["description"].Value().(string)
	f.mu.Unlock()
	if desc == "" {
		t.Error("shortcut bound without a description (KDE drops those)")
	}

	press := func(session dbus.ObjectPath, id string) {
		t.Helper()
		if err := srv.Emit(portalPath, shortcutsIface+".Activated", session, id, uint64(1), map[string]dbus.Variant{}); err != nil {
			t.Fatal(err)
		}
	}
	press("/org/freedesktop/portal/desktop/session/other/x", "moments-clip") // another app's
	press(sess, "unknown")
	press(sess, "quickset-profile-1")
	expectRun("ggctl profile 1")
	expectNoRun("for another session's or an unknown shortcut")

	// The same binds again change nothing; others take a new session.
	p.Set(map[string]Bind{
		"CTRL,F1": {ID: "quickset-profile-1", Command: "ggctl profile 1", Description: "QuickSet: switch to profile 1"},
		"ALT,S":   {ID: "moments-clip", Command: "ggctl moments clip", Description: "Moments: save a clip"},
	})
	p.Set(map[string]Bind{"SHIFT ALT,M": {ID: "moments-mic", Command: "ggctl moments mic", Description: "Moments: mute the microphone"}})
	expect("Close")
	expect("CreateSession " + sessionToken)
	expect("Bind moments-mic=SHIFT+ALT+m")
	press(sess, "moments-mic")
	expectRun("ggctl moments mic")

	// Nothing to bind closes the session.
	p.Set(nil)
	expect("Close")
	press(sess, "moments-mic")
	expectNoRun("after the shortcuts were dropped")
}

// TestNoPortal: without a GlobalShortcuts portal there is no Portal.
func TestNoPortal(t *testing.T) {
	cli, err := dbus.Connect(privateBus(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if _, err := newPortal(context.Background(), cli, func(string) {}); err == nil {
		t.Error("a portal on a bus without one")
	}
}
