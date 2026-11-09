// Package hotkeys makes GG's keyboard shortcuts global. Wayland gives
// applications no way to grab keys, so on Hyprland ggd asks the compositor
// to bind them, through hyprctl, to a command that tells ggd; the binds
// come back after Hyprland reloads its configuration.
package hotkeys

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Spec turns a GG shortcut (a HID keyboard usage and a HID modifier mask)
// into a Hyprland key ("ALT,S"); ok is false for keys it can't name.
func Spec(modifiers, hidCode int) (string, bool) {
	key, ok := hidKey(hidCode)
	if !ok {
		return "", false
	}
	var mods []string
	m := modifiers | modifiers>>4 // right-hand modifiers count as left
	if m&1 != 0 {
		mods = append(mods, "CTRL")
	}
	if m&2 != 0 {
		mods = append(mods, "SHIFT")
	}
	if m&4 != 0 {
		mods = append(mods, "ALT")
	}
	if m&8 != 0 {
		mods = append(mods, "SUPER")
	}
	return strings.Join(mods, " ") + "," + key, true
}

var namedKeys = map[int]string{
	40: "Return", 41: "Escape", 42: "BackSpace", 43: "Tab", 44: "space", 45: "minus", 46: "equal",
	47: "bracketleft", 48: "bracketright", 49: "backslash", 51: "semicolon", 52: "apostrophe", 53: "grave",
	54: "comma", 55: "period", 56: "slash", 70: "Print", 71: "Scroll_Lock", 72: "Pause", 73: "Insert",
	74: "Home", 75: "Prior", 76: "Delete", 77: "End", 78: "Next", 79: "Right", 80: "Left", 81: "Down", 82: "Up",
	84: "KP_Divide", 85: "KP_Multiply", 86: "KP_Subtract", 87: "KP_Add", 88: "KP_Enter",
}

// hidKey names a HID keyboard usage as an XKB key.
func hidKey(code int) (string, bool) {
	switch {
	case code >= 4 && code <= 29:
		return string(rune('A' + code - 4)), true
	case code >= 30 && code <= 38:
		return string(rune('1' + code - 30)), true
	case code == 39:
		return "0", true
	case code >= 58 && code <= 69:
		return fmt.Sprintf("F%d", code-57), true
	case code >= 104 && code <= 115:
		return fmt.Sprintf("F%d", code-91), true
	case code >= 89 && code <= 97:
		return fmt.Sprintf("KP_%d", code-88), true
	case code == 98:
		return "KP_0", true
	}
	k, ok := namedKeys[code]
	return k, ok
}

// Available reports whether this session can have global shortcuts.
func Available() bool { return os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" }

// Hyprland keeps a set of key binds in Hyprland.
type Hyprland struct {
	mu    sync.Mutex
	want  map[string]string // key spec → command
	bound map[string]string
}

// NewHyprland starts following Hyprland's configuration reloads, which
// drop binds made at run time.
func NewHyprland(ctx context.Context) *Hyprland {
	h := &Hyprland{want: map[string]string{}, bound: map[string]string{}}
	go h.watch(ctx)
	go func() {
		<-ctx.Done()
		h.Set(nil)
	}()
	return h
}

// Set makes exactly binds bound (by ggd; the user's own are left alone).
func (h *Hyprland) Set(binds map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.want = map[string]string{}
	for k, v := range binds {
		h.want[k] = v
	}
	h.applyLocked()
}

func (h *Hyprland) applyLocked() {
	for spec := range h.bound {
		if cmd, ok := h.want[spec]; !ok || cmd != h.bound[spec] {
			if err := hyprctl("keyword", "unbind", spec); err != nil {
				slog.Warn("hotkeys: unbind", "key", spec, "err", err)
			}
			delete(h.bound, spec)
		}
	}
	if len(h.want) == 0 {
		return
	}
	taken := userBinds()
	specs := make([]string, 0, len(h.want))
	for spec := range h.want {
		specs = append(specs, spec)
	}
	sort.Strings(specs)
	for _, spec := range specs {
		cmd := h.want[spec]
		if h.bound[spec] == cmd {
			continue
		}
		if taken[normalize(spec)] {
			slog.Warn("hotkeys: key already bound in Hyprland's configuration; leaving it", "key", spec)
			continue
		}
		if err := hyprctl("keyword", "bind", spec+",exec,"+cmd); err != nil {
			slog.Warn("hotkeys: bind", "key", spec, "err", err)
			continue
		}
		h.bound[spec] = cmd
		slog.Info("hotkeys: bound", "key", spec, "command", cmd)
	}
}

func hyprctl(args ...string) error {
	out, err := exec.Command("hyprctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	if s := strings.TrimSpace(string(out)); s != "ok" && s != "" {
		return fmt.Errorf("hyprctl: %s", s)
	}
	return nil
}

// Hyprland's modifier mask bits.
var hyprMods = map[string]int{"SHIFT": 1, "CTRL": 4, "ALT": 8, "SUPER": 64}

func normalize(spec string) string {
	mods, key, _ := strings.Cut(spec, ",")
	mask := 0
	for _, m := range strings.Fields(mods) {
		mask |= hyprMods[m]
	}
	return fmt.Sprintf("%d,%s", mask, strings.ToLower(key))
}

// userBinds lists the keys Hyprland has bound, other than to ggd.
func userBinds() map[string]bool {
	out, err := exec.Command("hyprctl", "-j", "binds").Output()
	if err != nil {
		return nil
	}
	var binds []struct {
		Modmask    int    `json:"modmask"`
		Key        string `json:"key"`
		Dispatcher string `json:"dispatcher"`
		Arg        string `json:"arg"`
	}
	if json.Unmarshal(out, &binds) != nil {
		return nil
	}
	taken := map[string]bool{}
	for _, b := range binds {
		if b.Dispatcher == "exec" && strings.Contains(b.Arg, "ggctl") {
			continue
		}
		taken[fmt.Sprintf("%d,%s", b.Modmask, strings.ToLower(b.Key))] = true
	}
	return taken
}

// watch re-binds after Hyprland reloads its configuration.
func (h *Hyprland) watch(ctx context.Context) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	sock := filepath.Join(dir, "hypr", os.Getenv("HYPRLAND_INSTANCE_SIGNATURE"), ".socket2.sock")
	for ctx.Err() == nil {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "unix", sock)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
			continue
		}
		go func() { <-ctx.Done(); conn.Close() }()
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "configreloaded>>") {
				h.mu.Lock()
				h.bound = map[string]string{}
				h.applyLocked()
				h.mu.Unlock()
			}
		}
		conn.Close()
	}
}
