package gamesense

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Device is a lit device as GameSense sees it: its types and named zones
// from its specification (the codes are HID codes on a per-key keyboard,
// zone numbers on other devices) and, for a keyboard, the 22x6 picture
// bitmap handlers paint (the code at each place, -1 where there is no
// key).
type Device struct {
	ProductID uint32
	Types     []string
	Zones     map[string][]int
	Grid      []int

	// A screen's size (0 without one).
	ScreenW, ScreenH int
}

func (d *Device) has(t string) bool {
	for _, x := range d.Types {
		if x == t {
			return true
		}
	}
	return false
}

var zonedType = regexp.MustCompile(`^rgb-\d+-zone$`)

func (d *Device) zoned() bool {
	for _, t := range d.Types {
		if zonedType.MatchString(t) {
			return true
		}
	}
	return false
}

func (d *Device) perKey() bool { return d.has("rgb-per-key-zones") }

// codes is the keys or zones a handler lights on a device, in order, or
// nil when it does not apply to the device.
func (h *Handler) codes(d *Device) []int {
	switch {
	case h.DeviceID != 0:
		if d.ProductID != h.DeviceID {
			return nil
		}
	case h.DeviceType == "rgb-zoned-device":
		if !d.zoned() {
			return nil
		}
	case !d.has(h.DeviceType):
		return nil
	}
	all := d.Zones["all"]
	switch {
	case h.HasCustomKeys:
		in := map[int]bool{}
		for _, c := range all {
			in[c] = true
		}
		var out []int
		for _, c := range h.CustomKeys {
			if in[c] {
				out = append(out, c)
			}
		}
		return out
	case h.Zone != "":
		return d.Zones[h.Zone]
	}
	return all
}

// DefaultDeinitialize is how long a game stays active after its last
// event or heartbeat, unless its metadata says otherwise.
const DefaultDeinitialize = 15 * time.Second

// State is the games registered with GameSense and their lighting.
type State struct {
	mu       sync.Mutex
	games    map[string]*game
	disabled map[string]bool
	hidden   map[string]bool // the Engine's own games (ImageSync), not listed
	seq      uint64

	// Now is the clock (tests set it).
	Now func() time.Time
	// OnActive is told when a game starts or stops lighting devices. It
	// is called without the state locked.
	OnActive func(game string, active bool)
}

type game struct {
	name, displayName, developer string
	deinit                       time.Duration
	events                       map[string]*event
	active                       bool
	last                         time.Time
	priority                     int
}

type event struct {
	min, max      float64
	valueOptional bool
	iconID        int
	handlers      []*binding
	user          map[uint32]*binding // the user's handler for a device, in place of the game's
	screens       []*screenBinding
}

// binding is a handler and the last value it showed.
type binding struct {
	h      *Handler
	set    bool
	value  float64
	frame  map[string]json.RawMessage
	color  Color
	half   int // flash half period, ms (0: steady)
	repeat int // flashes before staying on (0: forever)
	start  time.Time
	seq    uint64
}

// New makes an empty state.
func New() *State {
	return &State{games: map[string]*game{}, disabled: map[string]bool{}, hidden: map[string]bool{}, Now: time.Now}
}

func (s *State) game(name string) *game {
	name = strings.ToUpper(name)
	g, ok := s.games[name]
	if !ok {
		g = &game{name: name, deinit: DefaultDeinitialize, events: map[string]*event{}}
		s.games[name] = g
	}
	return g
}

func (g *game) event(name string) *event {
	name = strings.ToUpper(name)
	e, ok := g.events[name]
	if !ok {
		e = &event{max: 100}
		g.events[name] = e
	}
	return e
}

// Metadata is game_metadata. A deinitialize time out of 1-60 s is
// ignored.
func (s *State) Metadata(name, displayName, developer string, deinitMs int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.game(name)
	g.displayName, g.developer = displayName, developer
	if deinitMs >= 1000 && deinitMs <= 60000 {
		g.deinit = time.Duration(deinitMs) * time.Millisecond
	}
}

// EventInfo is what register_game_event and bind_game_event say about an
// event.
type EventInfo struct {
	Min, Max      float64
	ValueOptional bool
	IconID        int
}

// Register is register_game_event.
func (s *State) Register(gameName, eventName string, info EventInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.game(gameName).event(eventName)
	e.min, e.max, e.valueOptional, e.iconID = info.Min, info.Max, info.ValueOptional, info.IconID
}

// Bind is bind_game_event: the event's handlers are replaced. It returns
// how many of them are lighting handlers.
func (s *State) Bind(gameName, eventName string, info EventInfo, handlers []json.RawMessage) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.game(gameName).event(eventName)
	e.min, e.max, e.valueOptional, e.iconID = info.Min, info.Max, info.ValueOptional, info.IconID
	e.handlers, e.screens = nil, nil
	for _, raw := range handlers {
		if h, ok := ParseHandler(raw); ok {
			e.handlers = append(e.handlers, &binding{h: h})
		} else if sh, ok := parseScreenHandler(raw); ok {
			e.screens = append(e.screens, &screenBinding{h: sh})
		}
	}
	return len(e.handlers) + len(e.screens)
}

// RemoveEvent is remove_game_event.
func (s *State) RemoveEvent(gameName, eventName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g, ok := s.games[strings.ToUpper(gameName)]; ok {
		delete(g.events, strings.ToUpper(eventName))
	}
}

// RemoveGame is remove_game.
func (s *State) RemoveGame(gameName string) {
	s.mu.Lock()
	g, ok := s.games[strings.ToUpper(gameName)]
	delete(s.games, strings.ToUpper(gameName))
	s.mu.Unlock()
	if ok && g.active {
		s.notify([]change{{g.name, false}})
	}
}

type change struct {
	game   string
	active bool
}

func (s *State) notify(cs []change) {
	if s.OnActive == nil {
		return
	}
	for _, c := range cs {
		s.OnActive(c.game, c.active)
	}
}

// touch keeps a game active (and starts it).
func (s *State) touch(g *game, now time.Time) []change {
	g.last = now
	if g.active {
		return nil
	}
	g.active = true
	return []change{{g.name, true}}
}

func (g *game) deactivate() {
	g.active = false
	for _, e := range g.events {
		for _, b := range e.all() {
			b.set, b.frame = false, nil
		}
		for _, b := range e.screens {
			b.set, b.frame = false, nil
		}
	}
}

// all is the event's handlers and the user's.
func (e *event) all() []*binding {
	out := append([]*binding(nil), e.handlers...)
	for _, b := range e.user {
		out = append(out, b)
	}
	return out
}

// expire stops games whose time ran out.
func (s *State) expire(now time.Time) []change {
	var cs []change
	for _, g := range s.games {
		if g.active && now.Sub(g.last) > g.deinit {
			g.deactivate()
			cs = append(cs, change{g.name, false})
		}
	}
	return cs
}

// Heartbeat is game_heartbeat: it keeps an active game active.
func (s *State) Heartbeat(gameName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g, ok := s.games[strings.ToUpper(gameName)]; ok && g.active {
		g.last = s.Now()
	}
}

// Stop is stop_game: the game's lighting ends at once.
func (s *State) Stop(gameName string) {
	s.mu.Lock()
	g, ok := s.games[strings.ToUpper(gameName)]
	was := ok && g.active
	if ok {
		g.deactivate()
	}
	s.mu.Unlock()
	if was {
		s.notify([]change{{g.name, false}})
	}
}

// SetEnabled turns a game's lighting on or off (GG's switch for it).
func (s *State) SetEnabled(gameName string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		delete(s.disabled, strings.ToUpper(gameName))
	} else {
		s.disabled[strings.ToUpper(gameName)] = true
	}
}

// Enabled reports GG's switch for a game.
func (s *State) Enabled(gameName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.disabled[strings.ToUpper(gameName)]
}

// Event is game_event: data is the event's "data", an object (or a
// string holding one) with a "value" and an optional "frame".
func (s *State) Event(gameName, eventName string, data json.RawMessage) {
	var d struct {
		Value *float64                   `json:"value"`
		Frame map[string]json.RawMessage `json:"frame"`
	}
	if raw := strings.TrimSpace(string(data)); strings.HasPrefix(raw, `"`) {
		var inner string
		if json.Unmarshal(data, &inner) == nil {
			data = json.RawMessage(inner)
		}
	}
	json.Unmarshal(data, &d)
	var cs []change
	s.mu.Lock()
	now := s.Now()
	g := s.game(gameName)
	if s.disabled[g.name] {
		s.mu.Unlock()
		return
	}
	cs = s.touch(g, now)
	e := g.events[strings.ToUpper(eventName)]
	if e != nil {
		var v float64
		if d.Value != nil {
			v = *d.Value
		}
		for _, b := range e.all() {
			opt := e.valueOptional || b.h.valueOptional()
			if b.set && b.value == v && d.Value != nil && !opt {
				continue
			}
			s.seq++
			*b = binding{h: b.h, set: true, value: v, frame: d.Frame, color: b.h.color(v, opt),
				half: b.h.freq(v), repeat: b.h.repeat(v), start: now, seq: s.seq}
		}
		for _, b := range e.screens {
			if b.set && b.value == v && d.Value != nil && !e.valueOptional {
				continue
			}
			s.seq++
			b.update(v, d.Frame, now, s.seq)
		}
	}
	s.mu.Unlock()
	s.notify(cs)
}

// Active reports whether any game is lighting devices.
func (s *State) Active() bool {
	s.mu.Lock()
	cs := s.expire(s.Now())
	active := false
	for _, g := range s.games {
		active = active || (g.active && !s.disabled[g.name])
	}
	s.mu.Unlock()
	s.notify(cs)
	return active
}

// paint is one handler's colours on a device.
type paint struct {
	seq    uint64
	codes  []int
	colors []Color
}

// Lights is the colours the active games put on a device, by key or zone
// code. Where handlers overlap, the latest event wins.
func (s *State) Lights(d *Device) map[int]Color {
	s.mu.Lock()
	now := s.Now()
	cs := s.expire(now)
	var paints []paint
	for _, g := range s.games {
		if !g.active || s.disabled[g.name] {
			continue
		}
		for _, e := range g.events {
			hs := e.handlers
			if u := e.user[d.ProductID]; u != nil {
				hs = []*binding{u}
			}
			for _, b := range hs {
				if !b.set {
					continue
				}
				if p, ok := b.paint(d, g, now); ok {
					paints = append(paints, p)
				}
			}
		}
	}
	s.mu.Unlock()
	s.notify(cs)
	if len(paints) == 0 {
		return nil
	}
	sort.Slice(paints, func(i, j int) bool { return paints[i].seq < paints[j].seq })
	out := map[int]Color{}
	for _, p := range paints {
		for i, c := range p.codes {
			out[c] = p.colors[i]
		}
	}
	return out
}

// off reports whether a flashing handler is in its dark half.
func (b *binding) off(now time.Time) bool {
	if b.half <= 0 {
		return false
	}
	phase := int(now.Sub(b.start) / (time.Duration(b.half) * time.Millisecond))
	return phase%2 == 1 && (b.repeat == 0 || phase/2 < b.repeat)
}

func (b *binding) paint(d *Device, g *game, now time.Time) (paint, bool) {
	h := b.h
	switch h.Mode {
	case "disabled":
		return paint{}, false
	case "bitmap", "partial-bitmap":
		if !d.perKey() || len(d.Grid) == 0 || (h.DeviceID == 0 && !d.has(h.DeviceType)) || (h.DeviceID != 0 && h.DeviceID != d.ProductID) {
			return paint{}, false
		}
		pix := bitmap(b.frame)
		if len(pix) != 132 {
			return paint{}, false
		}
		excluded := map[int]bool{}
		if h.Mode == "partial-bitmap" {
			names := h.ExcludedEvents
			if raw, ok := b.frame["excluded-events"]; ok {
				names = nil
				json.Unmarshal(raw, &names)
			}
			for _, n := range names {
				if e := g.events[strings.ToUpper(n)]; e != nil {
					for _, o := range e.handlers {
						for _, c := range o.h.codes(d) {
							excluded[c] = true
						}
					}
				}
			}
		}
		p := paint{seq: b.seq}
		for i, code := range d.Grid {
			if code >= 0 && i < len(pix) && !excluded[code] {
				p.codes = append(p.codes, code)
				p.colors = append(p.colors, pix[i])
			}
		}
		return p, true
	}
	codes := b.h.codes(d)
	if len(codes) == 0 {
		return paint{}, false
	}
	c := b.color
	if h.Mode == "context-color" {
		c = frameColor(b.frame, h.ContextKey)
	}
	if b.off(now) {
		c = black
	}
	colors := make([]Color, len(codes))
	switch {
	case h.Mode == "percent" && d.perKey():
		percentColors(colors, c, b.value)
	case h.Mode == "count" && d.perKey():
		for i := range colors {
			if float64(i) < b.value {
				colors[i] = c
			}
		}
	default:
		for i := range colors {
			colors[i] = c
		}
	}
	return paint{seq: b.seq, codes: codes, colors: colors}, true
}

// percentColors is show-percent-on-zone: the first keys lit for the
// percentage, the last of them dimmed for the part of it it stands for.
func percentColors(out []Color, c Color, percent float64) {
	n := len(out)
	per := 100 / float64(n)
	var lit int
	switch {
	case percent <= 0:
		lit = 0
	case percent <= per:
		lit = 1
	default:
		lit = min(n, int((per+percent)/100*float64(n)))
	}
	rem := percent - per*float64(max(lit-1, 0))
	scale := min(int(100*rem/per), 100)
	last := Color{uint8(int(c.R) * scale / 100), uint8(int(c.G) * scale / 100), uint8(int(c.B) * scale / 100)}
	for i := range out {
		switch {
		case i < lit-1:
			out[i] = c
		case i == lit-1:
			out[i] = last
		default:
			out[i] = black
		}
	}
}

// frameColor is a context-color handler's colour: the frame's
// {"red", "green", "blue"} under its key, else black.
func frameColor(frame map[string]json.RawMessage, key string) Color {
	raw, ok := frame[key]
	if !ok {
		return black
	}
	var c struct {
		Red, Green, Blue *float64
	}
	if json.Unmarshal(raw, &c) != nil || c.Red == nil || c.Green == nil || c.Blue == nil {
		return black
	}
	return rgb{*c.Red, *c.Green, *c.Blue}.color()
}

// bitmap reads a frame's 22x6 "bitmap": colours as [r, g, b] (or
// {"red", "green", "blue"}).
func bitmap(frame map[string]json.RawMessage) []Color {
	var raw []json.RawMessage
	if json.Unmarshal(frame["bitmap"], &raw) != nil {
		return nil
	}
	out := make([]Color, len(raw))
	for i, r := range raw {
		var l []float64
		if json.Unmarshal(r, &l) == nil && len(l) >= 3 {
			out[i] = rgb{l[0], l[1], l[2]}.color()
			continue
		}
		var o rgb
		if json.Unmarshal(r, &o) == nil {
			out[i] = o.color()
		}
	}
	return out
}

// GameInfo describes a registered game for GG's GameSense page.
type GameInfo struct {
	Name, DisplayName, Developer string
	Enabled, Active              bool
	Events                       []string
}

// Games lists the registered games.
func (s *State) Games() []GameInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []GameInfo
	for _, g := range s.games {
		if s.hidden[g.name] {
			continue
		}
		gi := GameInfo{Name: g.name, DisplayName: g.displayName, Developer: g.developer, Enabled: !s.disabled[g.name], Active: g.active}
		for n := range g.events {
			gi.Events = append(gi.Events, n)
		}
		sort.Strings(gi.Events)
		out = append(out, gi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SetUserHandler sets the user's lighting handler for an event on a
// device (GG's GameSense page), which replaces the game's handlers there.
// An empty handler ("{}") gives the event back to the game.
func (s *State) SetUserHandler(gameName, eventName string, productID uint32, raw json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.game(gameName).event(eventName)
	h, ok := ParseHandler(raw)
	if !ok {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil && len(m) > 0 {
			// A handler GG's page made but that lights nothing (no
			// device type): keep the event dark on the device.
			h, ok = &Handler{DeviceID: productID, Mode: "disabled", Raw: append(json.RawMessage(nil), raw...)}, true
		}
	}
	if !ok {
		delete(e.user, productID)
		return
	}
	if e.user == nil {
		e.user = map[uint32]*binding{}
	}
	e.user[productID] = &binding{h: h}
}

// EventDetail describes an event for GG's GameSense page.
type EventDetail struct {
	Name     string
	Info     EventInfo
	Handlers []*Handler          // the game's lighting handlers
	User     map[uint32]*Handler // the user's, by device
}

// GameDetail describes a game for GG's GameSense page.
type GameDetail struct {
	GameInfo
	Events []EventDetail
}

// Details lists the registered games with their events.
func (s *State) Details() []GameDetail {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []GameDetail
	for _, g := range s.games {
		if s.hidden[g.name] {
			continue
		}
		gd := GameDetail{GameInfo: GameInfo{Name: g.name, DisplayName: g.displayName, Developer: g.developer, Enabled: !s.disabled[g.name], Active: g.active}}
		for n, e := range g.events {
			ed := EventDetail{Name: n, Info: EventInfo{Min: e.min, Max: e.max, ValueOptional: e.valueOptional, IconID: e.iconID}, User: map[uint32]*Handler{}}
			for _, b := range e.handlers {
				ed.Handlers = append(ed.Handlers, b.h)
			}
			for pid, b := range e.user {
				ed.User[pid] = b.h
			}
			gd.Events = append(gd.Events, ed)
			gd.GameInfo.Events = append(gd.GameInfo.Events, n)
		}
		sort.Slice(gd.Events, func(i, j int) bool { return gd.Events[i].Name < gd.Events[j].Name })
		sort.Strings(gd.GameInfo.Events)
		out = append(out, gd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Applies reports whether a handler lights anything on a device.
func (h *Handler) Applies(d *Device) bool {
	switch h.Mode {
	case "bitmap", "partial-bitmap":
		return d.perKey() && (h.DeviceID == d.ProductID || (h.DeviceID == 0 && d.has(h.DeviceType)))
	}
	return len(h.codes(d)) > 0
}

// Hide keeps one of the Engine's own games (ImageSync) out of the lists
// GG shows.
func (s *State) Hide(gameName string) {
	s.mu.Lock()
	s.hidden[strings.ToUpper(gameName)] = true
	s.mu.Unlock()
}
