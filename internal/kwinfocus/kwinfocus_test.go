package kwinfocus

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"ggl/internal/dbustest"
)

// TestScriptInKWinAPIs runs the script with node and a stand-in for
// KWin's script API, Plasma 6's and Plasma 5's, and checks what it sends.
func TestScriptInKWinAPIs(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runs the script in this test:", err)
	}
	harness := `
const calls = [];
function callDBus() { calls.push(Array.from(arguments)); }
function signal() { const fs = []; return { connect: f => fs.push(f), fire: w => fs.forEach(f => f(w)) }; }
const game = { pid: 4242, resourceClass: "wow.exe", caption: "World of Warcraft" };
const desk = { pid: 1717, resourceClass: "org.kde.dolphin", caption: "Home — Dolphin" };
let workspace;
if (process.env.PLASMA === "6") {
	workspace = { windowActivated: signal(), activeWindow: desk };
} else {
	workspace = { clientActivated: signal(), activeClient: desk };
}
eval(require("fs").readFileSync(process.argv[2], "utf8"));
(workspace.windowActivated || workspace.clientActivated).fire(game);
(workspace.windowActivated || workspace.clientActivated).fire(null);
console.log(JSON.stringify(calls));
`
	dir := t.TempDir()
	h := filepath.Join(dir, "harness.js")
	s := filepath.Join(dir, "script.js")
	os.WriteFile(h, []byte(harness), 0o644)
	os.WriteFile(s, []byte(Script(`:1.42`)), 0o644)
	row := func(args ...any) []any { return append([]any{":1.42", objectPath, iface, "Activated"}, args...) }
	want := [][]any{
		row("1717", "org.kde.dolphin", ""), // titles are left out
		row("4242", "wow.exe", ""),
		row("0", "", ""),
	}
	for _, plasma := range []string{"6", "5"} {
		cmd := exec.Command(node, h, s)
		cmd.Env = append(os.Environ(), "PLASMA="+plasma)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Plasma %s: %v\n%s", plasma, err, out)
		}
		var got [][]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("Plasma %s: %v: %s", plasma, err, out)
		}
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		if string(g) != string(w) {
			t.Errorf("Plasma %s calls\n %s\nwant\n %s", plasma, g, w)
		}
	}
}

func TestScriptQuotesItsDestination(t *testing.T) {
	s := Script(`a"b\c`)
	if !strings.Contains(s, `callDBus("a\"b\\c", "/io/github/gglinux/Foreground", "io.github.gglinux.Foreground", "Activated"`) {
		t.Errorf("script:\n%s", s)
	}
}

// fakeKWin stands in for KWin's scripting interface.
type fakeKWin struct {
	conn *dbus.Conn
	mu   sync.Mutex
	log  []string
	dest string
}

func (k *fakeKWin) record(s string) {
	k.mu.Lock()
	k.log = append(k.log, s)
	k.mu.Unlock()
}

func (k *fakeKWin) calls() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.log...)
}

var destRE = regexp.MustCompile(`callDBus\("([^"]+)"`)

func startFakeKWin(t *testing.T, addr string, refuse bool) *fakeKWin {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	k := &fakeKWin{conn: conn}
	conn.ExportMethodTable(map[string]any{
		"loadScript": func(path, name string) (int32, *dbus.Error) {
			b, err := os.ReadFile(path)
			if err != nil {
				return -1, nil
			}
			k.record("load " + name)
			if refuse {
				return -1, nil
			}
			k.mu.Lock()
			k.dest = destRE.FindStringSubmatch(string(b))[1]
			k.mu.Unlock()
			return 0, nil
		},
		"start": func() *dbus.Error {
			k.record("start")
			// The script reports the active window as it starts.
			go k.activate("4242", "wow.exe", "World of Warcraft")
			return nil
		},
		"unloadScript": func(name string) (bool, *dbus.Error) {
			k.record("unload " + name)
			return true, nil
		},
	}, scriptingPath, scriptingIf)
	if _, err := conn.RequestName(kwinName, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	return k
}

func (k *fakeKWin) activate(pid, class, caption string) error {
	k.mu.Lock()
	dest := k.dest
	k.mu.Unlock()
	return k.conn.Object(dest, objectPath).Call(iface+".Activated", 0, pid, class, caption).Err
}

// sessionBus starts a private session bus for the test.
func sessionBus(t *testing.T) string {
	addr := dbustest.Bus(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	return addr
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out waiting for", what)
}

func TestTracker(t *testing.T) {
	addr := sessionBus(t)
	_, plugin, err := scriptFile()
	if err != nil {
		t.Fatal(err)
	}
	if tr, err := Start(); err == nil {
		tr.Close()
		t.Fatal("started without KWin")
	}

	kwin := startFakeKWin(t, addr, false)
	tr, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first window", func() bool { _, ok := tr.Window(); return ok })
	if w, _ := tr.Window(); w != (Window{PID: 4242, Class: "wow.exe"}) {
		t.Errorf("window %+v", w)
	}
	select {
	case <-tr.Changed():
	default:
		t.Error("no change signalled")
	}
	if got := strings.Join(kwin.calls(), "; "); got != "unload "+plugin+"; load "+plugin+"; start" {
		t.Errorf("KWin calls: %s", got)
	}

	// Nobody but KWin can say which window is in front.
	other, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.Object(tr.conn.Names()[0], objectPath).Call(iface+".Activated", 0, "1", "x", "y").Err; err == nil {
		t.Error("a stranger set the window")
	}
	if err := kwin.activate("0", "", ""); err != nil {
		t.Fatal(err)
	}
	if w, ok := tr.Window(); !ok || w.PID != 0 {
		t.Errorf("after the desktop got focus: %+v %v", w, ok)
	}

	// KWin restarting takes the script with it: the window is unknown
	// until the new KWin has the script again.
	kwin.conn.Close()
	waitFor(t, "KWin gone", func() bool { _, ok := tr.Window(); return !ok })
	kwin2 := startFakeKWin(t, addr, false)
	waitFor(t, "the script reloaded", func() bool { _, ok := tr.Window(); return ok })
	if got := strings.Join(kwin2.calls(), "; "); got != "unload "+plugin+"; load "+plugin+"; start" {
		t.Errorf("new KWin calls: %s", got)
	}

	file := tr.file
	if _, err := os.Stat(file); err != nil {
		t.Error("no script file:", err)
	}
	tr.Close()
	waitFor(t, "unload", func() bool { c := kwin2.calls(); return c[len(c)-1] == "unload "+plugin })
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("script file left behind")
	}
}

func TestTrackerScriptRefused(t *testing.T) {
	addr := sessionBus(t)
	startFakeKWin(t, addr, true)
	if tr, err := Start(); err == nil {
		tr.Close()
		t.Fatal("started with the script refused")
	}
	var none *Tracker
	if _, ok := none.Window(); ok || none.Changed() != nil {
		t.Error("a nil tracker knows a window")
	}
	none.Close()
}
