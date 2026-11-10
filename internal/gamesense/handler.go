// Package gamesense runs the lighting side of the GameSense SDK: the JSON
// handlers games bind to their events, and the colours they put on each
// device's keys and zones. The engine feeds it the games' requests;
// Prism draws its colours over its own.
//
// The rules follow the Windows Engine's GameSense library (its JSON
// handler compiler and the per-key keyboard interface): see Handler.
package gamesense

import (
	"encoding/json"
	"math"
	"strings"
)

// Color is an RGB colour.
type Color struct{ R, G, B uint8 }

var black = Color{}

// Handler is a lighting handler as a game binds it:
//
//	{"device-type": "keyboard", "zone": "function-keys", "mode": "percent",
//	 "color": {"gradient": {"zero": {...}, "hundred": {...}}},
//	 "rate": {"frequency": 2, "repeat_limit": 5}}
//
// Modes: "color" (the default) lights the zone, "percent" a share of its
// keys and "count" that many of them (both on per-key devices; other
// devices show the colour), "context-color" takes the colour from the
// event's frame, "bitmap" and "partial-bitmap" paint a 22x6 picture on a
// keyboard, and "disabled" lights nothing.
type Handler struct {
	DeviceType     string
	DeviceID       uint32 // a product id instead of a type
	Zone           string
	CustomKeys     []int
	HasCustomKeys  bool
	Mode           string
	ContextKey     string
	ExcludedEvents []string

	Raw json.RawMessage // as bound

	color  colorDef
	freq   rangedInt // half period in ms
	repeat rangedInt
}

// ParseHandler reads one entry of a bind_game_event "handlers" list. It
// reports false for handlers that are not lighting (tactile, screen).
func ParseHandler(raw json.RawMessage) (*Handler, bool) {
	var j struct {
		DeviceType     string          `json:"device-type"`
		DeviceID       json.Number     `json:"device-id"`
		Zone           any             `json:"zone"`
		CustomKeys     []json.Number   `json:"custom-zone-keys"`
		Mode           string          `json:"mode"`
		Color          json.RawMessage `json:"color"`
		Rate           json.RawMessage `json:"rate"`
		ContextKey     string          `json:"context-frame-key"`
		ExcludedEvents []string        `json:"excluded-events"`
		Datas          json.RawMessage `json:"datas"`
		Pattern        json.RawMessage `json:"pattern"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if d.Decode(&j) != nil {
		return nil, false
	}
	if j.Datas != nil || j.Pattern != nil || strings.HasPrefix(j.DeviceType, "screened") || j.DeviceType == "tactile" {
		return nil, false
	}
	h := &Handler{DeviceType: j.DeviceType, Mode: j.Mode, ContextKey: strings.TrimSuffix(j.ContextKey, ":"), ExcludedEvents: j.ExcludedEvents,
		Raw: append(json.RawMessage(nil), raw...)}
	if h.Mode == "" {
		h.Mode = "color"
	}
	if id, err := j.DeviceID.Int64(); err == nil {
		h.DeviceID = uint32(id)
	}
	if s, ok := j.Zone.(string); ok {
		h.Zone = strings.TrimSuffix(s, ":")
	}
	if j.CustomKeys != nil {
		h.HasCustomKeys = true
		for _, n := range j.CustomKeys {
			if v, err := n.Int64(); err == nil {
				h.CustomKeys = append(h.CustomKeys, int(v))
			}
		}
	}
	if h.DeviceType == "" && h.DeviceID == 0 {
		return nil, false
	}
	h.color = parseColor(j.Color, true)
	var rate struct {
		Frequency   json.RawMessage `json:"frequency"`
		RepeatLimit json.RawMessage `json:"repeat_limit"`
	}
	json.Unmarshal(j.Rate, &rate)
	h.freq = parseRanged(rate.Frequency, "frequency", func(f float64) int {
		if f <= 0 {
			return 0
		}
		return int(500 / f)
	})
	h.repeat = parseRanged(rate.RepeatLimit, "repeat_limit", func(f float64) int { return int(f) })
	return h, true
}

// valueOptional is whether the handler runs on every event, even one
// with an unchanged value (the frame modes always do).
func (h *Handler) valueOptional() bool {
	return h.Mode == "bitmap" || h.Mode == "partial-bitmap" || h.Mode == "context-color"
}

// colorDef is a compiled colour definition: the colour for an event
// value.
type colorDef func(v float64, valueOptional bool) Color

// parseColor compiles a colour definition. A static colour is black for
// a zero value at the top level (unless the value is optional), but not
// inside a range.
func parseColor(raw json.RawMessage, top bool) colorDef {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return func(float64, bool) Color { return black }
	}
	if raw[0] == '[' {
		var ranges []struct {
			Low   float64         `json:"low"`
			High  float64         `json:"high"`
			Color json.RawMessage `json:"color"`
		}
		if json.Unmarshal(raw, &ranges) != nil || len(ranges) == 0 {
			return func(float64, bool) Color { return black }
		}
		lowest, highest := math.Inf(1), math.Inf(-1)
		for _, r := range ranges {
			lowest, highest = math.Min(lowest, r.Low), math.Max(highest, r.High)
		}
		type clause struct {
			low, high float64
			open      [2]bool
			c         colorDef
		}
		var cs []clause
		for _, r := range ranges {
			cs = append(cs, clause{r.Low, r.High, [2]bool{r.Low == lowest, r.High == highest}, parseColor(r.Color, false)})
		}
		return func(v float64, opt bool) Color {
			for _, c := range cs {
				if (c.open[0] || v >= c.low) && (c.open[1] || v <= c.high) {
					return c.c(v, opt)
				}
			}
			return black
		}
	}
	var o struct {
		Gradient *struct {
			Zero    rgb `json:"zero"`
			Hundred rgb `json:"hundred"`
		} `json:"gradient"`
		Red *float64 `json:"red"`
	}
	var static rgb
	if json.Unmarshal(raw, &o) != nil || json.Unmarshal(raw, &static) != nil {
		return func(float64, bool) Color { return black }
	}
	if g := o.Gradient; g != nil {
		return func(v float64, _ bool) Color { return between(g.Zero.color(), g.Hundred.color(), v) }
	}
	if o.Red == nil {
		return func(float64, bool) Color { return black }
	}
	c := static.color()
	if !top {
		return func(float64, bool) Color { return c }
	}
	return func(v float64, opt bool) Color {
		if v == 0 && !opt {
			return black
		}
		return c
	}
}

// rgb is a {"red", "green", "blue"} colour.
type rgb struct {
	Red   float64 `json:"red"`
	Green float64 `json:"green"`
	Blue  float64 `json:"blue"`
}

func (c rgb) color() Color { return Color{clamp(c.Red), clamp(c.Green), clamp(c.Blue)} }

func clamp(v float64) uint8 { return uint8(math.Max(0, math.Min(255, v))) }

// between is color-between: the point percent of the way from zero to
// hundred, truncated.
func between(zero, hundred Color, percent float64) Color {
	e := percent / 100
	s := 1 - e
	mix := func(a, b uint8) uint8 { return clamp(math.Trunc(s*float64(a) + e*float64(b))) }
	return Color{mix(zero.R, hundred.R), mix(zero.G, hundred.G), mix(zero.B, hundred.B)}
}

// rangedInt is a rate setting: a number, or ranges of the event value
// with one each (0 outside them).
type rangedInt func(v float64) int

func parseRanged(raw json.RawMessage, key string, conv func(float64) int) rangedInt {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return func(float64) int { return 0 }
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		x := conv(n)
		return func(float64) int { return x }
	}
	var ranges []map[string]float64
	if json.Unmarshal(raw, &ranges) != nil {
		return func(float64) int { return 0 }
	}
	return func(v float64) int {
		for _, r := range ranges {
			if v >= r["low"] && v <= r["high"] {
				return conv(r[key])
			}
		}
		return 0
	}
}
