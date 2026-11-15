// Package kwinfocus follows the window in front on KDE Plasma. Wayland
// gives clients no way to ask which window is active, but KWin runs
// scripts that can: ggd loads a small one over KWin's D-Bus scripting
// interface, and the script calls ggd back with the pid, class and title
// of every window KWin activates. It covers Wayland windows and X11 ones
// (Xwayland: Wine and Proton games) alike.
package kwinfocus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	kwinName      = "org.kde.KWin"
	scriptingPath = "/Scripting"
	scriptingIf   = "org.kde.kwin.Scripting"
	pluginName    = "gg-linux-foreground"

	objectPath = "/io/github/gglinux/Foreground"
	iface      = "io.github.gglinux.Foreground"
)

// Window is the window in front. PID is what KWin knows: the Wayland
// client's pid, or for X11 windows the pid Xwayland or the window reports
// (a sandboxed X11 client can report the pid it has in its own pid
// namespace). PID 0 means no window is active.
type Window struct {
	PID     int
	Class   string // resource class: an app id, "wow.exe", "steam_app_570"
	Caption string
}

// Tracker follows KWin's active window. A nil Tracker knows nothing.
type Tracker struct {
	conn    *dbus.Conn
	file    string
	changed chan struct{}

	mu    sync.Mutex
	kwin  string // KWin's unique bus name; only it may report windows
	win   Window
	known bool
}

// Script is the KWin script that reports active windows to the D-Bus
// connection named dest. It uses Plasma 6's names and falls back to
// Plasma 5's (clientActivated, activeClient), where at least the pid
// comes through. Values go as strings, which match the method's fixed
// signature whatever types KWin gives script values on D-Bus.
func Script(dest string) string {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	return fmt.Sprintf(`// gg-linux: tells ggd which window is in front, for QuickSet's game profiles.
(function () {
    function send(w) {
        if (w) {
            callDBus(%[1]s, %[2]s, %[3]s, "Activated", String(w.pid), String(w.resourceClass), String(w.caption));
        } else {
            callDBus(%[1]s, %[2]s, %[3]s, "Activated", "0", "", "");
        }
    }
    (workspace.windowActivated || workspace.clientActivated).connect(send);
    send(workspace.activeWindow !== undefined ? workspace.activeWindow : workspace.activeClient);
})();
`, q(dest), q(objectPath), q(iface))
}

// Start loads the script into KWin. It fails where there is no KWin on
// the session bus or it refuses the script; callers then do without.
func Start() (*Tracker, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	t := &Tracker{conn: conn, changed: make(chan struct{}, 1), file: scriptFile()}
	if err := conn.ExportMethodTable(map[string]any{"Activated": t.activated}, objectPath, iface); err != nil {
		conn.Close()
		return nil, err
	}
	// Learn of KWin restarting: its scripts go with it.
	conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, kwinName),
	)
	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)
	if err := t.load(); err != nil {
		t.Close()
		return nil, err
	}
	go t.watchKWin(signals)
	return t, nil
}

// scriptFile is where the script goes for KWin to read: the runtime
// directory, which a distrobox shares with the host.
func scriptFile() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "gg-linux-foreground.js")
}

func (t *Tracker) call(method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return t.conn.Object(kwinName, scriptingPath).CallWithContext(ctx, scriptingIf+"."+method, 0, args...)
}

// load (re)loads the script into the running KWin.
func (t *Tracker) load() error {
	var owner string
	if err := t.conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, kwinName).Store(&owner); err != nil {
		return fmt.Errorf("no KWin on the session bus: %w", err)
	}
	t.mu.Lock()
	t.kwin, t.known = owner, false
	t.mu.Unlock()
	if err := os.WriteFile(t.file, []byte(Script(t.conn.Names()[0])), 0o644); err != nil {
		return err
	}
	// A ggd that did not exit cleanly left its script loaded; KWin will
	// not load another under the same name.
	t.call("unloadScript", pluginName)
	var id int32
	if err := t.call("loadScript", t.file, pluginName).Store(&id); err != nil {
		return fmt.Errorf("KWin loadScript: %w", err)
	}
	if id < 0 {
		return fmt.Errorf("KWin refused the script (%d)", id)
	}
	if err := t.call("start").Err; err != nil {
		t.call("unloadScript", pluginName)
		return fmt.Errorf("KWin start: %w", err)
	}
	slog.Info("kwinfocus: following the active window through KWin")
	return nil
}

func (t *Tracker) watchKWin(signals chan *dbus.Signal) {
	for s := range signals {
		if s.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(s.Body) != 3 {
			continue
		}
		newOwner, _ := s.Body[2].(string)
		t.mu.Lock()
		t.kwin, t.known = "", false
		t.mu.Unlock()
		t.notify()
		if newOwner == "" {
			slog.Info("kwinfocus: KWin left the bus")
			continue
		}
		if err := t.load(); err != nil {
			slog.Warn("kwinfocus: reloading the script into the new KWin", "err", err)
		}
	}
}

// activated is the method the script calls.
func (t *Tracker) activated(sender dbus.Sender, pid, class, caption string) *dbus.Error {
	t.mu.Lock()
	if string(sender) != t.kwin {
		t.mu.Unlock()
		return dbus.MakeFailedError(fmt.Errorf("only KWin reports windows"))
	}
	n, _ := strconv.Atoi(pid)
	w := Window{PID: n, Class: class, Caption: caption}
	same := t.known && t.win == w
	t.win, t.known = w, true
	t.mu.Unlock()
	if !same {
		slog.Debug("kwinfocus: active window", "pid", n, "class", class, "caption", caption)
		t.notify()
	}
	return nil
}

func (t *Tracker) notify() {
	select {
	case t.changed <- struct{}{}:
	default:
	}
}

// Window is the window in front; ok is false until KWin has said.
func (t *Tracker) Window() (w Window, ok bool) {
	if t == nil {
		return Window{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.win, t.known
}

// Changed receives when the window in front changes.
func (t *Tracker) Changed() <-chan struct{} {
	if t == nil {
		return nil
	}
	return t.changed
}

// Close takes the script out of KWin.
func (t *Tracker) Close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	running := t.kwin != ""
	t.mu.Unlock()
	if running {
		t.call("unloadScript", pluginName)
	}
	os.Remove(t.file)
	t.conn.Close()
}
