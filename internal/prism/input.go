package prism

import (
	"encoding/json"
	"log/slog"
	"time"

	"ggl/internal/engine"
)

// The idle and reactive layers follow input: after the configuration's
// idle time without any, the idle layer replaces the base one; a key
// press starts the reactive layer's graphic at that key. Input is only
// watched while the current configuration uses either layer.

// reaction is a key press being shown.
type reaction struct {
	at     time.Time
	local  bool   // only the key pressed shows it
	zone   string // the key pressed
	render *compiled
}

// triggerConfig is libsteel.TriggerConfig.
type triggerConfig struct {
	IsLocal   bool   `json:"isLocal"`
	GraphicID string `json:"graphicId"`
}

// Reactions last at most this long.
const maxReaction = 10 * time.Second

// usesInput reports whether a configuration needs input: an idle time,
// or a reactive layer with a trigger.
func usesInput(c *ConfigData) bool {
	if c.IdleTimeMs > 0 {
		return true
	}
	has := func(s *Single) bool { return len(s.TriggerConfig) > 0 && string(s.TriggerConfig) != "null" }
	g := &c.Reactive
	if has(&g.GlobalConfig) {
		return true
	}
	for i := range g.DeviceTypeConfigs {
		if has(&g.DeviceTypeConfigs[i].Config) {
			return true
		}
	}
	for i := range g.PerKeyConfigs {
		if has(&g.PerKeyConfigs[i].Config) {
			return true
		}
	}
	for i := range g.DeviceConfigs {
		if has(&g.DeviceConfigs[i].Config) {
			return true
		}
	}
	return false
}

// watchInput starts or stops following input as the configuration needs.
func (s *Service) watchInput(on bool) {
	s.mu.Lock()
	changed := s.watching != on
	s.watching = on
	if on && changed {
		s.lastInput = time.Now()
	}
	s.mu.Unlock()
	if !changed || s.eng == nil {
		return
	}
	if on {
		slog.Info("prism: following key presses and activity for the idle and reactive layers")
		s.eng.WatchPrismInput(s.onInput)
	} else {
		slog.Info("prism: no longer following input")
		s.eng.WatchPrismInput(nil)
	}
}

// onInput notes activity and starts the reactions to keys pressed.
func (s *Service) onInput(in engine.PrismInput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastInput = time.Now()
	cur := s.current
	if cur == nil || len(in.Pressed) == 0 {
		return
	}
	for _, key := range in.Pressed {
		// The key on the keyboard pressed, else on any keyboard lit.
		var dev *device
		var z *zone
		for _, d := range s.devices {
			for i := range d.Zones {
				if d.Zones[i].HidCode == key && d.HasBitmapCoordinates && (z == nil || d.ID == in.DeviceID) {
					dev, z = d, &d.Zones[i]
				}
			}
		}
		if z == nil || !dev.Enabled {
			continue
		}
		cfg := cur.Data.Reactive.resolve(dev, z)
		var tc triggerConfig
		if len(cfg.TriggerConfig) == 0 || json.Unmarshal(cfg.TriggerConfig, &tc) != nil || tc.GraphicID == "" {
			continue
		}
		g := cfg.graphic(tc.GraphicID)
		if g == nil {
			continue
		}
		s.reactions = append(s.reactions, reaction{at: time.Now(), local: tc.IsLocal, zone: z.UniqueID, render: compileGraphic(g, z.pos())})
	}
	if n := len(s.reactions); n > 64 {
		s.reactions = s.reactions[n-64:]
	}
}

// layerFor is the layer to draw now: idle once the idle time has passed
// without input.
func (s *Service) layerFor(c *ConfigData, lastInput time.Time) *Group {
	if c.IdleTimeMs > 0 && time.Since(lastInput) > time.Duration(c.IdleTimeMs)*time.Millisecond {
		return &c.Idle
	}
	return &c.Base
}

// react draws the key press reactions over the colours, dropping the
// ones that are over.
func (s *Service) react(colors map[string]Color, devs []*device) {
	s.mu.Lock()
	rs := s.reactions
	s.mu.Unlock()
	if len(rs) == 0 {
		return
	}
	var keep []reaction
	for _, r := range rs {
		t := time.Since(r.at)
		if t > maxReaction {
			continue
		}
		shown := false
		for _, d := range devs {
			for i := range d.Zones {
				z := &d.Zones[i]
				if r.local && z.UniqueID != r.zone {
					continue
				}
				c, op, ok := r.render.overlay(z.pos(), t.Milliseconds())
				if !ok {
					continue
				}
				colors[z.UniqueID] = mix(colors[z.UniqueID], c, op)
				shown = true
			}
		}
		// A wave that reached nothing yet may still be starting.
		if shown || t < time.Second {
			keep = append(keep, r)
		}
	}
	s.mu.Lock()
	// Reactions added meanwhile stay.
	if len(s.reactions) >= len(rs) {
		keep = append(keep, s.reactions[len(rs):]...)
	}
	s.reactions = keep
	s.mu.Unlock()
}
