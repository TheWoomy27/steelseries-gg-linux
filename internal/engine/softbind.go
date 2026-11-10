package engine

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"ggl/internal/devicert"
	"ggl/internal/hid"
	"ggl/internal/hidkeys"
	"ggl/internal/vinput"
)

// Software bindings: keys and buttons bound to something their device
// can't do by itself (a macro on a mouse without macro memory, launching
// an application, switching configurations, typing a text). On Windows
// the SteelSeries driver takes the button's input away from the system
// and the Engine runs the binding. On Linux:
//
//   - When the configuration is deployed, each key or button bound
//     (unless passthrough) is remapped to send a spare keyboard usage,
//     which the desktop sees as an unknown key nothing acts on. A device
//     whose mappings can't be remapped keeps its input, and the binding
//     runs as well (as passthrough bindings do).
//   - ggd reads the device's input reports (hidraw) while it has
//     bindings, and runs a binding when its key or button goes down (or
//     up, for bindings set to play on release).
//   - Macros play through a virtual keyboard and mouse (uinput).

// softBinding is a binding ggd runs.
type softBinding struct {
	trig   devicert.Trigger
	play   devicert.Playback
	spare  uint8 // the usage the device sends instead, 0 for none
	pass   bool  // passthrough: the key keeps its own input too
	events []devicert.MacroEvent
	cmd    *sseCommand

	mu      sync.Mutex
	running chan struct{} // closed to stop a repeating or toggled run
	held    bool          // a hold binding is down
}

type sseCommand struct {
	ID, Type, Name, Path, Params string
}

type softState struct {
	watchMu  sync.Mutex // one rewatch at a time
	mu       sync.Mutex
	devices  map[int][]*softBinding // by Engine device id
	sigs     map[int]string         // what each device's bindings were made from
	remove   []func()
	player   inputPlayer
	noPlayer error
	warned   map[string]bool
}

// softBindingsFor reads the bindings of a configuration ggd runs for a
// device: Engine commands, and macros where the device has no macro
// memory to play them itself.
func (e *Engine) softBindingsFor(l *liveDevice, configID string) []*softBinding {
	onboard := l.cat != nil && l.cat.HasOnboardMacros
	var out []*softBinding
	for _, b := range e.bindingsOf(configID) {
		var sb softBinding
		if json.Unmarshal([]byte(b.Trigger), &sb.trig) != nil {
			continue
		}
		sb.pass = b.IsPassthrough
		switch {
		case b.SseCommandID != "":
			c, ok := e.sseCommand(b.SseCommandID)
			if !ok {
				continue
			}
			sb.cmd = &c
			sb.play = devicert.Playback{NumRepeats: 1}
			json.Unmarshal([]byte(orEmpty(b.PlaybackOptionsData, defaultPlayback)), &playbackJSON{&sb.play})
		default:
			if onboard {
				continue // the device plays these itself
			}
			mb, ok := e.macroBinding(b)
			if !ok {
				continue
			}
			sb.play, sb.events = mb.Playback, mb.Events
		}
		out = append(out, &sb)
	}
	// Spare usages for the bindings that take the key's input away, in
	// order, so a redeploy gives each the same one.
	n := 0
	for _, sb := range out {
		if sb.pass || !remappable(sb.trig) || n >= len(hidkeys.Spare) {
			continue
		}
		sb.spare = hidkeys.Spare[n]
		n++
	}
	return out
}

// remappable reports whether a trigger is an input a device mapping can
// send: a mouse button, the wheel, or a key (not the meta layer).
func remappable(t devicert.Trigger) bool {
	switch t.Type {
	case 0:
		return t.HIDCode >= 1 && t.HIDCode <= 8
	case 1:
		return t.HIDCode == 0x31 || t.HIDCode == 0x32
	case 2:
		return t.Page == 1 && t.HIDCode > 3 && t.HIDCode <= 0xff
	}
	return false
}

func (e *Engine) sseCommand(id string) (sseCommand, bool) {
	var c sseCommand
	err := e.db.QueryRow(`SELECT CAST(id AS TEXT), command_type, display_name, path, params FROM sse_commands WHERE id = ?`, id).
		Scan(&c.ID, &c.Type, &c.Name, &c.Path, &c.Params)
	return c, err == nil
}

// applySoftBindings reads the device's current bindings, remaps their
// keys in the settings about to be written, and follows the device's
// input for them.
func (e *Engine) applySoftBindings(l *liveDevice, settings map[string]any) map[string]any {
	var binds []*softBinding
	sig := ""
	if cfg, err := e.activeConfig(l.dbID); err == nil {
		binds = e.softBindingsFor(l, cfg.ID)
		b, _ := json.Marshal(e.bindingsOf(cfg.ID))
		sig = cfg.ID + string(b)
	}
	out := settings
	if v, changed := remapOutputs(settings, binds); changed {
		out = v.(map[string]any)
	}
	st := &e.soft
	st.mu.Lock()
	if st.devices == nil {
		st.devices, st.sigs = map[int][]*softBinding{}, map[int]string{}
	}
	same := st.sigs[l.dbID] == sig
	if !same {
		for _, old := range st.devices[l.dbID] {
			old.stop()
		}
		if len(binds) > 0 {
			st.devices[l.dbID], st.sigs[l.dbID] = binds, sig
		} else {
			delete(st.devices, l.dbID)
			delete(st.sigs, l.dbID)
		}
	}
	st.mu.Unlock()
	if !same {
		if len(binds) > 0 {
			slog.Info("bindings ggd runs", "device", l.cat.Name, "count", len(binds))
			if slices.ContainsFunc(binds, func(b *softBinding) bool { return b.cmd == nil || b.cmd.Type == "text_macro" }) {
				go e.player() // ready before the first press
			}
		}
		go e.rewatchSoft()
	}
	return out
}

// remapOutputs points the mapping entries ({function, key_codes}) that
// send a bound key or button at the binding's spare usage, as a combo key
// (function 0x51). Settings are copied where they change.
func remapOutputs(v any, binds []*softBinding) (any, bool) {
	switch x := v.(type) {
	case map[string]any:
		if fn, ok := num(x["function"]); ok {
			if codes, ok := x["key_codes"].([]any); ok {
				for _, b := range binds {
					if b.spare != 0 && sends(fn, codes, b.trig) {
						m := make(map[string]any, len(x))
						for k, e := range x {
							m[k] = e
						}
						nc := make([]any, max(len(codes), 1))
						for i := range nc {
							nc[i] = float64(0)
						}
						nc[0] = float64(b.spare)
						m["function"], m["key_codes"] = float64(0x51), nc
						return m, true
					}
				}
				return x, false
			}
		}
		var out map[string]any
		for k, e := range x {
			if ne, changed := remapOutputs(e, binds); changed {
				if out == nil {
					out = make(map[string]any, len(x))
					for k2, e2 := range x {
						out[k2] = e2
					}
				}
				out[k] = ne
			}
		}
		if out != nil {
			return out, true
		}
	case []any:
		var out []any
		for i, e := range x {
			if ne, changed := remapOutputs(e, binds); changed {
				if out == nil {
					out = slices.Clone(x)
				}
				out[i] = ne
			}
		}
		if out != nil {
			return out, true
		}
	}
	return v, false
}

// sends reports whether a mapping entry sends a trigger's input.
func sends(fn float64, codes []any, t devicert.Trigger) bool {
	switch t.Type {
	case 0:
		return fn == float64(t.HIDCode)
	case 1:
		return fn == float64(t.HIDCode) // 0x31 up, 0x32 down
	case 2:
		if fn != 0x51 || len(codes) == 0 {
			return false
		}
		if c, _ := num(codes[0]); c != float64(t.HIDCode) {
			return false
		}
		for _, c := range codes[1:] {
			if n, _ := num(c); n != 0 {
				return false
			}
		}
		return true
	}
	return false
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// rewatchSoft follows the input of the connected devices with bindings.
func (e *Engine) rewatchSoft() {
	st := &e.soft
	st.watchMu.Lock()
	defer st.watchMu.Unlock()
	st.mu.Lock()
	for _, r := range st.remove {
		r()
	}
	st.remove = nil
	devices := make(map[int][]*softBinding, len(st.devices))
	for id, b := range st.devices {
		devices[id] = b
	}
	st.mu.Unlock()
	var removers []func()
	for id, binds := range devices {
		l := e.liveByID(id)
		if l == nil {
			continue
		}
		for _, d := range l.decls() {
			for _, node := range e.hid.Devices(uint16(d.ID & 0xffff)) {
				desc := node.Desc
				if len(desc.Keys) == 0 && len(desc.Buttons) == 0 && len(desc.Extra) == 0 {
					continue
				}
				keyIDs, buttonIDs, extraIDs := map[uint8]bool{}, map[uint8]bool{}, map[uint8]bool{}
				for _, f := range desc.Keys {
					keyIDs[f.ReportID] = true
				}
				for _, f := range desc.Buttons {
					buttonIDs[f.ReportID] = true
				}
				for _, f := range desc.Extra {
					extraIDs[f.ReportID] = true
				}
				heldKeys, heldButtons, heldExtra := map[uint8]map[uint8]bool{}, map[uint8]map[uint8]bool{}, map[uint8]map[uint8]bool{}
				binds := binds
				name := l.cat.Name
				logged := false
				removers = append(removers, node.Listen(func(r []byte) {
					if len(r) == 0 {
						return
					}
					id := r[0]
					if extraIDs[id] {
						now := set(desc.ExtraButtons(r))
						if !logged && extraSet(desc, r) {
							// Once, to confirm where the device reports them.
							slog.Info("extra mouse buttons in the pointer report", "device", name, "report", fmt.Sprintf("% x", r))
							logged = true
						}
						edges(heldExtra[id], now, func(bit uint8, down bool) { e.extraEdge(binds, bit, down) })
						heldExtra[id] = now
					}
					if keyIDs[id] {
						now := set(desc.PressedKeys(r))
						edges(heldKeys[id], now, func(u uint8, down bool) { e.keyEdge(binds, u, down) })
						heldKeys[id] = now
					}
					if buttonIDs[id] {
						now := set(desc.PressedButtons(r))
						edges(heldButtons[id], now, func(n uint8, down bool) { e.buttonEdge(binds, n, down) })
						heldButtons[id] = now
					}
				}))
			}
		}
	}
	st.mu.Lock()
	st.remove = removers
	st.mu.Unlock()
}

func set(xs []uint8) map[uint8]bool {
	m := make(map[uint8]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// edges calls fn for each key that went down or up between two reports.
func edges(before, now map[uint8]bool, fn func(uint8, bool)) {
	for k := range now {
		if !before[k] {
			fn(k, true)
		}
	}
	for k := range before {
		if !now[k] {
			fn(k, false)
		}
	}
}

func (e *Engine) keyEdge(binds []*softBinding, usage uint8, down bool) {
	for _, b := range binds {
		if b.spare != 0 && b.spare == usage || b.trig.Type == 2 && b.trig.Page == 1 && b.trig.HIDCode == uint16(usage) {
			e.trigger(b, down)
		}
	}
}

// extraEdge runs the bindings of a mouse's extra button (trigger type
// 13, by its bit). GG switches such a button's own output off when it is
// bound, and the mouse reports it in its vendor byte only.
func (e *Engine) extraEdge(binds []*softBinding, bit uint8, down bool) {
	for _, b := range binds {
		if b.trig.Type == 13 && b.trig.HIDCode == uint16(bit) {
			e.trigger(b, down)
		}
	}
}

func (e *Engine) buttonEdge(binds []*softBinding, n uint8, down bool) {
	for _, b := range binds {
		if b.trig.Type == 0 && b.trig.HIDCode == uint16(n) {
			e.trigger(b, down)
		}
	}
}

// trigger runs a binding as its key or button goes down or up, by its
// playback options (play once, repeat N times, repeat while pressed,
// toggle repeat, toggle hold) and trigger direction (1 on press, 0 on
// release).
func (e *Engine) trigger(b *softBinding, down bool) {
	if hold := holdEvents(b.events); len(hold) > 0 && b.play.Type == 0 {
		// A key or button held for as long as the trigger is.
		go e.playHold(hold, down)
		return
	}
	fire := down == (b.trig.Direction != 0)
	switch b.play.Type {
	case 2: // repeat while pressed
		if down {
			b.start(e, -1)
		} else {
			b.stop()
		}
	case 3: // toggle repeat
		if fire && !b.stop() {
			b.start(e, -1)
		}
	case 4: // toggle hold
		if fire {
			b.mu.Lock()
			held := b.held
			b.held = !held
			b.mu.Unlock()
			if held {
				go e.release(b.events)
			} else {
				go e.play(b.events, nil, true)
			}
		}
	case 1: // repeat N times
		if fire {
			b.start(e, max(int(b.play.NumRepeats), 1))
		}
	default:
		if fire {
			b.start(e, 1)
		}
	}
}

// start runs a binding times times (-1: until stopped), unless it is
// running already.
func (b *softBinding) start(e *Engine, times int) {
	b.mu.Lock()
	if b.running != nil {
		b.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	b.running = stop
	b.mu.Unlock()
	go func() {
		defer func() {
			b.mu.Lock()
			if b.running == stop {
				b.running = nil
			}
			b.mu.Unlock()
		}()
		for i := 0; times < 0 || i < times; i++ {
			if i > 0 && !sleep(time.Duration(b.play.RepeatDelay)*time.Millisecond, stop) {
				return
			}
			if b.cmd != nil {
				e.runCommand(b.cmd)
				if times < 0 {
					return // commands don't repeat
				}
			} else if !e.play(b.events, stop, false) {
				return
			}
		}
	}()
}

// stop ends a running binding, reporting whether one was running.
func (b *softBinding) stop() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running == nil {
		return false
	}
	close(b.running)
	b.running = nil
	return true
}

func sleep(d time.Duration, stop <-chan struct{}) bool {
	if d <= 0 {
		select {
		case <-stop:
			return false
		default:
			return true
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}

func holdEvents(evs []devicert.MacroEvent) []devicert.MacroEvent {
	var out []devicert.MacroEvent
	for _, ev := range evs {
		if ev.Type == devicert.EventMouseHold || ev.Type == devicert.EventKeyHold {
			out = append(out, ev)
		}
	}
	return out
}

// inputPlayer is what macros play through (vinput.Device).
type inputPlayer interface {
	Key(code uint16, down bool) error
	Wheel(n int32, horizontal bool) error
	Close()
}

// player is the virtual keyboard and mouse macros play through, made on
// first use.
func (e *Engine) player() inputPlayer {
	st := &e.soft
	st.mu.Lock()
	if st.player != nil || st.noPlayer != nil {
		p := st.player
		st.mu.Unlock()
		return p
	}
	p, err := vinput.Open("SteelSeries GG virtual input")
	if err != nil {
		st.noPlayer = err
		st.mu.Unlock()
		slog.Warn("macros can't play (no access to /dev/uinput?)", "err", err)
		return nil
	}
	st.player = p
	st.mu.Unlock()
	// The desktop picks a new input device up a moment after it appears.
	time.Sleep(300 * time.Millisecond)
	return p
}

// eventKey is the Linux key or button an event presses, 0 for none.
func eventKey(ev devicert.MacroEvent) uint16 {
	switch ev.Type {
	case devicert.EventKey, devicert.EventKeyHold:
		switch ev.Page {
		case 1, 7:
			if ev.Code < 0x100 {
				return hidkeys.Keyboard[ev.Code]
			}
		case 12:
			return hidkeys.Consumer[ev.Code]
		}
	case devicert.EventMouseButton:
		return hidkeys.Button(ev.Code)
	case devicert.EventMouseHold:
		// The trigger's own button mask (1 << (n-1)).
		for n := uint16(1); n <= 8; n++ {
			if ev.Code == 1<<(n-1) {
				return hidkeys.Button(n)
			}
		}
	}
	return 0
}

// play plays a macro's events, until stop. Keys it presses and doesn't
// release are let go at the end, unless hold is set (toggle hold).
func (e *Engine) play(evs []devicert.MacroEvent, stop <-chan struct{}, hold bool) bool {
	p := e.player()
	if p == nil {
		return false
	}
	pressed := map[uint16]bool{}
	defer func() {
		if !hold {
			for k := range pressed {
				p.Key(k, false)
			}
		}
	}()
	for _, ev := range evs {
		switch ev.Type {
		case devicert.EventDelay:
			if !sleep(time.Duration(ev.ExtraData)*time.Millisecond, stop) {
				return false
			}
		case devicert.EventMouseWheel, devicert.EventACPan:
			d := int32(1)
			if ev.ExtraData == -1 || ev.ExtraData == 0xff || ev.ExtraData == 0xffffffff {
				d = -1
			} else if ev.ExtraData == 0 {
				continue
			}
			p.Wheel(d, ev.Type == devicert.EventACPan)
		default:
			k := eventKey(ev)
			if k == 0 {
				continue
			}
			down := ev.ExtraData != 0 || ev.Type == devicert.EventMouseHold || ev.Type == devicert.EventKeyHold
			if hold && !down {
				continue
			}
			p.Key(k, down)
			if down {
				pressed[k] = true
			} else {
				delete(pressed, k)
			}
		}
		select {
		case <-stop:
			return false
		default:
		}
	}
	return true
}

// release lets go of the keys a macro presses (the end of toggle hold).
func (e *Engine) release(evs []devicert.MacroEvent) {
	p := e.player()
	if p == nil {
		return
	}
	for _, ev := range evs {
		if k := eventKey(ev); k != 0 {
			p.Key(k, false)
		}
	}
}

func (e *Engine) playHold(evs []devicert.MacroEvent, down bool) {
	p := e.player()
	if p == nil {
		return
	}
	for _, ev := range evs {
		if k := eventKey(ev); k != 0 {
			p.Key(k, down)
		}
	}
}

// runCommand runs an Engine command binding.
func (e *Engine) runCommand(c *sseCommand) {
	switch c.Type {
	case "launch_application", "launchApplication":
		if err := launch(c.Path); err != nil {
			slog.Warn("launch application", "path", c.Path, "err", err)
		}
	case "deploy_configs":
		var p struct {
			ConfigIDs []string `json:"configIds"`
		}
		json.Unmarshal([]byte(c.Params), &p)
		for _, id := range p.ConfigIDs {
			cfg, err := e.configByID(id)
			if err != nil {
				continue
			}
			if err := e.deployConfig(cfg, false); err != nil {
				slog.Warn("deploy from a binding", "config", cfg.Name, "err", err)
			}
			e.configSelected(cfg)
		}
	case "text_macro":
		e.typeText(c.Params)
	case "game_event":
		e.gameEvent(c.Params)
	case "toggle_engine_apps":
		e.toggleEngineApps(c.Params)
	default:
		st := &e.soft
		st.mu.Lock()
		if st.warned == nil {
			st.warned = map[string]bool{}
		}
		first := !st.warned[c.Type]
		st.warned[c.Type] = true
		st.mu.Unlock()
		if first {
			slog.Info("binding not supported on Linux yet", "command", c.Type)
		}
	}
}

// launch starts an application: a .desktop file through its launcher,
// a program directly, anything else with its default application.
func launch(path string) error {
	if path == "" {
		return fmt.Errorf("no path")
	}
	var cmd *exec.Cmd
	st, err := os.Stat(path)
	switch {
	case strings.HasSuffix(path, ".desktop"):
		cmd = exec.Command("gio", "launch", path)
	case err == nil && !st.IsDir() && st.Mode()&0o111 != 0:
		cmd = exec.Command(path)
		cmd.Dir = filepath.Dir(path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// typeText plays a text macro as Windows GG does: its keys before, the
// text pasted (Ctrl+V) from the clipboard, its keys after.
func (e *Engine) typeText(params string) {
	var p struct {
		Text           string    `json:"text"`
		Delay          int       `json:"delay"`
		PreTextEvents  []textKey `json:"preTextEvents"`
		PostTextEvents []textKey `json:"postTextEvents"`
	}
	if json.Unmarshal([]byte(params), &p) != nil {
		return
	}
	pause := time.Duration(max(p.Delay, 20)) * time.Millisecond
	tap := func(keys []textKey) {
		for _, k := range keys {
			ev := devicert.MacroEvent{Type: devicert.EventKey, Page: 1, Code: k.Code}
			if strings.Contains(strings.ToLower(k.Type), "mouse") {
				ev = devicert.MacroEvent{Type: devicert.EventMouseButton, Code: k.Code}
			}
			down := ev
			down.ExtraData = 1
			e.play([]devicert.MacroEvent{down, {Type: devicert.EventDelay, ExtraData: 20}, ev}, nil, false)
			time.Sleep(pause)
		}
	}
	tap(p.PreTextEvents)
	if p.Text != "" {
		clip := exec.Command("wl-copy", "--", p.Text)
		if err := clip.Run(); err != nil {
			slog.Warn("text macro: no clipboard (wl-copy)", "err", err)
		} else {
			time.Sleep(pause)
			key := func(usage uint16, down int64) devicert.MacroEvent {
				return devicert.MacroEvent{Type: devicert.EventKey, Page: 1, Code: usage, ExtraData: down}
			}
			e.play([]devicert.MacroEvent{key(hidLeftCtrl, 1), key(0x19, 1), {Type: devicert.EventDelay, ExtraData: 20}, key(0x19, 0), key(hidLeftCtrl, 0)}, nil, false)
			time.Sleep(pause)
		}
	}
	tap(p.PostTextEvents)
}

type textKey struct {
	Type string `json:"type"`
	Code uint16 `json:"code"`
}

// closeSoft stops the bindings and removes the virtual input device.
func (e *Engine) closeSoft() {
	st := &e.soft
	st.mu.Lock()
	for _, r := range st.remove {
		r()
	}
	st.remove = nil
	for _, binds := range st.devices {
		for _, b := range binds {
			b.stop()
		}
	}
	p := st.player
	st.player = nil
	st.mu.Unlock()
	if p != nil {
		p.Close()
	}
}

// refreshSoftBindings rereads the bindings ggd runs (after a macro or an
// Engine command changes). Their keys keep their spare usages, so the
// devices need no redeploy.
func (e *Engine) refreshSoftBindings() {
	st := &e.soft
	st.mu.Lock()
	ids := make([]int, 0, len(st.devices))
	for id := range st.devices {
		ids = append(ids, id)
	}
	st.mu.Unlock()
	for _, id := range ids {
		l := e.liveByID(id)
		cfg, err := e.activeConfig(id)
		if l == nil || err != nil {
			continue
		}
		binds := e.softBindingsFor(l, cfg.ID)
		st.mu.Lock()
		for _, old := range st.devices[id] {
			old.stop()
		}
		if len(binds) > 0 {
			st.devices[id] = binds
		} else {
			delete(st.devices, id)
		}
		st.mu.Unlock()
	}
	go e.rewatchSoft()
}

// extraSet reports whether a pointer report has any vendor byte set past
// the one repeating buttons 1-8 (logged once, to show where a mouse
// reports its extra buttons).
func extraSet(desc hid.Descriptor, r []byte) bool {
	for _, f := range desc.Extra {
		if f.ReportID != r[0] || f.BitOffset%8 != 0 {
			continue
		}
		for i := f.BitOffset/8 + 1; i < f.BitOffset/8+f.Count && i+1 < len(r); i++ {
			if r[i+1] != 0 {
				return true
			}
		}
	}
	return false
}
