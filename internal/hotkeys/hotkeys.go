// Package hotkeys makes GG's keyboard shortcuts global. Wayland gives
// applications no way to grab keys, so on Hyprland ggd asks the compositor
// to bind them, through hyprctl, to a command that tells ggd; the binds
// come back after Hyprland reloads its configuration. Hyprland 0.57 and
// later take binds as Lua (hl.bind); older versions as keywords.
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
	"strconv"
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

// Bind is what a key does: a command, and the description Hyprland lists
// it with.
type Bind struct {
	Command, Description string
}

// descPrefix marks ggd's binds, so they are told apart from the user's.
const descPrefix = "SteelSeries GG"

// Hyprland keeps a set of key binds in Hyprland.
type Hyprland struct {
	mu    sync.Mutex
	lua   bool
	want  map[string]Bind // key spec → bind
	bound map[string]Bind
}

// NewHyprland starts following Hyprland's configuration reloads, which
// drop binds made at run time.
func NewHyprland(ctx context.Context) *Hyprland {
	h := &Hyprland{want: map[string]Bind{}, bound: map[string]Bind{}}
	h.lua = exec.Command("hyprctl", "eval", "return 1").Run() == nil
	// Binds an earlier ggd left behind (it stopped without cleaning up)
	// are dropped, so they don't block their keys.
	for spec := range ownBinds() {
		h.unbind(spec)
	}
	go h.watch(ctx)
	go func() {
		<-ctx.Done()
		h.Set(nil)
	}()
	return h
}

// Set makes exactly binds bound (by ggd; the user's own are left alone).
func (h *Hyprland) Set(binds map[string]Bind) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.want = map[string]Bind{}
	for k, v := range binds {
		h.want[k] = v
	}
	h.applyLocked()
}

func (h *Hyprland) applyLocked() {
	for spec, b := range h.bound {
		if w, ok := h.want[spec]; !ok || w != b {
			if err := h.unbind(spec); err != nil {
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
		b := h.want[spec]
		if h.bound[spec] == b {
			continue
		}
		if taken[normalize(spec)] {
			slog.Warn("hotkeys: key already bound in Hyprland's configuration; leaving it", "key", spec)
			continue
		}
		if err := h.bind(spec, b); err != nil {
			slog.Warn("hotkeys: bind", "key", spec, "err", err)
			continue
		}
		h.bound[spec] = b
		slog.Info("hotkeys: bound", "key", spec, "command", b.Command)
	}
}

// luaKeys writes a key spec ("SHIFT ALT,M") the Lua way ("SHIFT + ALT + M").
func luaKeys(spec string) string {
	mods, key, _ := strings.Cut(spec, ",")
	return strings.Join(append(strings.Fields(mods), key), " + ")
}

func (h *Hyprland) bind(spec string, b Bind) error {
	desc := descPrefix + ": " + b.Description
	if h.lua {
		return hyprctl("eval", fmt.Sprintf("hl.bind(%s, hl.dsp.exec_cmd(%s), { description = %s })",
			strconv.Quote(luaKeys(spec)), strconv.Quote(b.Command), strconv.Quote(desc)))
	}
	mods, key, _ := strings.Cut(spec, ",")
	return hyprctl("keyword", "bindd", mods+","+key+","+strings.ReplaceAll(desc, ",", " ")+",exec,"+b.Command)
}

func (h *Hyprland) unbind(spec string) error {
	if h.lua {
		return hyprctl("eval", fmt.Sprintf("hl.unbind(%s)", strconv.Quote(luaKeys(spec))))
	}
	return hyprctl("keyword", "unbind", spec)
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

type hyprBind struct {
	Modmask     int    `json:"modmask"`
	Key         string `json:"key"`
	Dispatcher  string `json:"dispatcher"`
	Arg         string `json:"arg"`
	Description string `json:"description"`
}

func (b hyprBind) ours() bool {
	return strings.HasPrefix(b.Description, descPrefix) || strings.Contains(b.Arg, "ggctl moments")
}

func listBinds() []hyprBind {
	out, err := exec.Command("hyprctl", "-j", "binds").Output()
	if err != nil {
		return nil
	}
	var binds []hyprBind
	json.Unmarshal(out, &binds)
	return binds
}

// userBinds lists the keys Hyprland has bound, other than for ggd.
func userBinds() map[string]bool {
	taken := map[string]bool{}
	for _, b := range listBinds() {
		if !b.ours() {
			taken[fmt.Sprintf("%d,%s", b.Modmask, strings.ToLower(b.Key))] = true
		}
	}
	return taken
}

// ownBinds lists the key specs bound for ggd.
func ownBinds() map[string]bool {
	own := map[string]bool{}
	for _, b := range listBinds() {
		if !b.ours() {
			continue
		}
		var mods []string
		for _, m := range []string{"CTRL", "SHIFT", "ALT", "SUPER"} {
			if b.Modmask&hyprMods[m] != 0 {
				mods = append(mods, m)
			}
		}
		own[strings.Join(mods, " ")+","+b.Key] = true
	}
	return own
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
				h.bound = map[string]Bind{}
				h.applyLocked()
				h.mu.Unlock()
			}
		}
		conn.Close()
	}
}
