package gamesense

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"ggl/internal/oled"
)

// Screen handlers put text, progress bars, icons or pictures on a
// device's screen (the Apex OLED):
//
//	{"device-type": "screened", "zone": "one", "mode": "screen",
//	 "datas": [{"lines": [{"has-text": true, "context-frame-key": "song", "bold": true},
//	                      {"has-progress-bar": true}],
//	            "icon-id": 1, "length-millis": 3000}]}
//
// "datas" is a list of frames shown one after another, each for its
// length-millis (0: until the next event), or ranges of the event value
// with a list each. After the last frame the sequence repeats as the last
// frame's "repeats" says (true: forever, a number: that many times),
// else the screen goes back to the device's own picture. This follows the
// Windows Engine's screen library.

// ScreenHandler is a screen handler as a game binds it.
type ScreenHandler struct {
	DeviceType string
	DeviceID   uint32
	Zone       string
	Disabled   bool // "screen-disabled": keeps the screen for the device's own picture
	Raw        json.RawMessage

	datas screenDatas
}

// screenFrame is one entry of "datas".
type screenFrame struct {
	LengthMillis int
	IconID       int
	Repeats      int // the sequence's repeats, from the last frame: 0 forever
	Image        []byte
	ImageFromCtx bool // takes image-data from the event's frame
	Lines        []screenLine
}

type screenLine struct {
	Text           bool
	Prefix, Suffix string
	Key            string
	Arg            *string
	Bar            bool
	Bold           bool
	Wrap           int
}

type screenDatas func(v float64) []screenFrame

// parseScreenHandler reads a screen handler; ok is false for other
// handlers.
func parseScreenHandler(raw json.RawMessage) (*ScreenHandler, bool) {
	var j struct {
		DeviceType string          `json:"device-type"`
		DeviceID   json.Number     `json:"device-id"`
		Zone       any             `json:"zone"`
		Mode       string          `json:"mode"`
		Datas      json.RawMessage `json:"datas"`
	}
	if json.Unmarshal(raw, &j) != nil {
		return nil, false
	}
	if j.Mode != "screen" && j.Mode != "screen-disabled" && !strings.HasPrefix(j.DeviceType, "screened") {
		return nil, false
	}
	h := &ScreenHandler{DeviceType: j.DeviceType, Disabled: j.Mode == "screen-disabled", Raw: append(json.RawMessage(nil), raw...)}
	if id, err := j.DeviceID.Int64(); err == nil {
		h.DeviceID = uint32(id)
	}
	if s, ok := j.Zone.(string); ok {
		h.Zone = strings.TrimSuffix(s, ":")
	}
	if h.DeviceType == "" && h.DeviceID == 0 {
		return nil, false
	}
	h.datas = parseDatas(j.Datas)
	return h, true
}

func parseDatas(raw json.RawMessage) screenDatas {
	var list []map[string]json.RawMessage
	if json.Unmarshal(raw, &list) != nil || len(list) == 0 {
		return func(float64) []screenFrame { return nil }
	}
	if _, ok := list[0]["low"]; ok {
		if _, ok := list[0]["datas"]; ok {
			type rng struct {
				low, high float64
				open      [2]bool
				datas     screenDatas
			}
			lowest, highest := math.Inf(1), math.Inf(-1)
			var rs []rng
			for _, r := range list {
				var lo, hi float64
				json.Unmarshal(r["low"], &lo)
				json.Unmarshal(r["high"], &hi)
				lowest, highest = math.Min(lowest, lo), math.Max(highest, hi)
				rs = append(rs, rng{low: lo, high: hi, datas: parseDatas(r["datas"])})
			}
			for i := range rs {
				rs[i].open = [2]bool{rs[i].low == lowest, rs[i].high == highest}
			}
			return func(v float64) []screenFrame {
				for _, r := range rs {
					if (r.open[0] || v >= r.low) && (r.open[1] || v <= r.high) {
						return r.datas(v)
					}
				}
				return nil
			}
		}
	}
	var frames []screenFrame
	for _, d := range list {
		frames = append(frames, parseFrame(d))
	}
	// The last frame's "repeats" sets the sequence's: true repeats
	// forever, false or none shows it once.
	reps := 1
	if r, ok := list[len(list)-1]["repeats"]; ok {
		var b bool
		var n int
		switch {
		case json.Unmarshal(r, &b) == nil:
			if b {
				reps = 0
			}
		case json.Unmarshal(r, &n) == nil:
			reps = n
		}
	}
	for i := range frames {
		frames[i].Repeats = reps
	}
	return func(float64) []screenFrame { return frames }
}

func parseFrame(d map[string]json.RawMessage) screenFrame {
	var f screenFrame
	json.Unmarshal(d["length-millis"], &f.LengthMillis)
	json.Unmarshal(d["icon-id"], &f.IconID)
	var hasText bool
	json.Unmarshal(d["has-text"], &hasText)
	if img, ok := d["image-data"]; ok && !hasText {
		var bytes []int
		if json.Unmarshal(img, &bytes) == nil {
			for _, b := range bytes {
				f.Image = append(f.Image, byte(b))
			}
		}
		f.ImageFromCtx = true
	}
	var lines []map[string]json.RawMessage
	if raw, ok := d["lines"]; ok {
		json.Unmarshal(raw, &lines)
	} else {
		lines = []map[string]json.RawMessage{d}
	}
	for _, l := range lines {
		var sl screenLine
		json.Unmarshal(l["has-text"], &sl.Text)
		json.Unmarshal(l["has-progress-bar"], &sl.Bar)
		json.Unmarshal(l["bold"], &sl.Bold)
		json.Unmarshal(l["wrap"], &sl.Wrap)
		sl.Prefix, sl.Suffix, sl.Key = jsonText(l["prefix"]), jsonText(l["suffix"]), strings.TrimSuffix(jsonText(l["context-frame-key"]), ":")
		if raw, ok := l["arg"]; ok {
			a := jsonText(raw)
			sl.Arg = &a
		}
		if sl.Text || sl.Bar {
			f.Lines = append(f.Lines, sl)
		}
	}
	return f
}

// jsonText is a JSON string's text, or a number's.
func jsonText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// screenBinding is a screen handler and what it is showing.
type screenBinding struct {
	h      *ScreenHandler
	set    bool
	value  float64
	frame  map[string]json.RawMessage
	frames []screenFrame
	start  time.Time
	seq    uint64
}

func (b *screenBinding) update(v float64, frame map[string]json.RawMessage, now time.Time, seq uint64) {
	*b = screenBinding{h: b.h, set: true, value: v, frame: frame, frames: b.h.datas(v), start: now, seq: seq}
}

// current is the frame shown at now, or nil once the sequence is over.
func (b *screenBinding) current(now time.Time) *screenFrame {
	if len(b.frames) == 0 {
		return nil
	}
	t := int(now.Sub(b.start) / time.Millisecond)
	cycle := 0
	for _, f := range b.frames {
		if f.LengthMillis <= 0 {
			cycle = -1
			break
		}
		cycle += f.LengthMillis
	}
	reps := b.frames[len(b.frames)-1].Repeats
	if cycle > 0 {
		done := t / cycle
		if reps > 0 && done >= reps {
			return nil
		}
		t %= cycle
	}
	for i := range b.frames {
		f := &b.frames[i]
		if f.LengthMillis <= 0 || t < f.LengthMillis {
			return f
		}
		t -= f.LengthMillis
	}
	return nil
}

// Applies reports whether a screen handler shows on a device.
func (h *ScreenHandler) Applies(d *Device) bool { return h.applies(d) }

func (h *ScreenHandler) applies(d *Device) bool {
	if d.ScreenW == 0 {
		return false
	}
	if h.DeviceID != 0 {
		return h.DeviceID == d.ProductID
	}
	return d.has(h.DeviceType) || h.DeviceType == "screened"
}

// Screen is what a game shows on a device's screen.
type Screen struct {
	Game   string
	IconID int
	Lines  []oled.Line
	Image  []byte // a whole packed picture instead of lines
}

// Screen is what the active games show on a device's screen now, or
// nil for the device's own picture. Games with a higher priority win;
// among equals the latest event does.
func (s *State) Screen(d *Device) *Screen {
	s.mu.Lock()
	now := s.Now()
	cs := s.expire(now)
	var best *screenBinding
	var bestGame *game
	for _, g := range s.games {
		if !g.active || s.disabled[g.name] {
			continue
		}
		for _, e := range g.events {
			for _, b := range e.screens {
				if !b.set || b.h.Disabled || !b.h.applies(d) || b.current(now) == nil {
					continue
				}
				if best == nil || g.priority > bestGame.priority || (g.priority == bestGame.priority && b.seq > best.seq) {
					best, bestGame = b, g
				}
			}
		}
	}
	var out *Screen
	if best != nil {
		out = best.render(bestGame.name, best.current(now), d)
	}
	s.mu.Unlock()
	s.notify(cs)
	return out
}

func (b *screenBinding) render(game string, f *screenFrame, d *Device) *Screen {
	sc := &Screen{Game: game, IconID: f.IconID}
	if f.ImageFromCtx {
		img := f.Image
		for _, key := range []string{fmt.Sprintf("image-data-%dx%d", d.ScreenW, d.ScreenH), "image-data"} {
			var bytes []int
			if json.Unmarshal(b.frame[key], &bytes) == nil && len(bytes) > 0 {
				img = nil
				for _, v := range bytes {
					img = append(img, byte(v))
				}
				break
			}
		}
		if len(img) == d.ScreenW*d.ScreenH/8 {
			sc.Image = img
			return sc
		}
	}
	for _, l := range f.Lines {
		ol := oled.Line{Bold: l.Bold, Wrap: l.Wrap}
		if l.Bar {
			ol.Bar = true
			v := b.value
			if l.Key != "" {
				v = 0
				json.Unmarshal(b.frame[l.Key], &v)
			}
			ol.Value = int(min(max(v, 0), 100))
		} else {
			val := formatValue(b.value)
			switch {
			case l.Arg != nil:
				val = *l.Arg
				if strings.Contains(val, "value") {
					val = formatValue(b.value)
				}
			case l.Key != "":
				val = ""
				if raw, ok := b.frame[l.Key]; ok {
					val = jsonText(raw)
				}
			}
			ol.Text = l.Prefix + val + l.Suffix
		}
		sc.Lines = append(sc.Lines, ol)
	}
	return sc
}

func formatValue(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// SetPriority ranks a game's screens against other games' (default 0;
// the Engine's own apps sit below games).
func (s *State) SetPriority(gameName string, p int) {
	s.mu.Lock()
	s.game(gameName).priority = p
	s.mu.Unlock()
}

// ScreenGame is the game showing on a device's screen, or "".
func (s *State) ScreenGame(d *Device) string {
	if sc := s.Screen(d); sc != nil {
		return sc.Game
	}
	return ""
}
