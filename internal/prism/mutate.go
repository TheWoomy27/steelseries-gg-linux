package prism

import (
	"encoding/json"
	"fmt"
	"slices"
)

// update is configuration_types.ConfigurationUpdateData with the client's
// zone selection: what an effect, a colour or a preset changes on the
// zones it is applied to.
type update struct {
	DefaultColor              *Color          `json:"defaultColor,omitempty"`
	Foundations               *[]Foundation   `json:"foundations,omitempty"`
	Graphics                  *[]Graphic      `json:"graphics,omitempty"`
	PerKeySelectionLimitation *[]BitmapArea   `json:"perKeySelectionLimitation,omitempty"`
	TriggerConfigs            json.RawMessage `json:"triggerConfigs,omitempty"`
	UniqueEffectType          *int            `json:"uniqueEffectType,omitempty"`
	Metadata                  *map[string]any `json:"metadata,omitempty"`
	UniqueEffectData          json.RawMessage `json:"uniqueEffectData,omitempty"`
	DimFactorOnMonitorSleep   *uint8          `json:"dimFactorOnMonitorSleep,omitempty"`
	ZoneSelection             *struct {
		ZoneIDs []string `json:"zoneIds"`
		Mode    string   `json:"mode"`
	} `json:"zoneSelection,omitempty"`
}

// parseSelection reads the zones an update body selects (its
// selectionInfo, as the client sends it); nil selects every zone.
func parseSelection(b []byte) []string {
	var body struct {
		SelectionInfo *struct {
			ZoneSelection *struct {
				ZoneIDs []string `json:"zoneIds"`
			} `json:"zoneSelection"`
		} `json:"selectionInfo"`
		ZoneSelection *struct {
			ZoneIDs []string `json:"zoneIds"`
		} `json:"zoneSelection"`
	}
	if json.Unmarshal(b, &body) != nil {
		return nil
	}
	if body.SelectionInfo != nil && body.SelectionInfo.ZoneSelection != nil {
		return body.SelectionInfo.ZoneSelection.ZoneIDs
	}
	if body.ZoneSelection != nil {
		return body.ZoneSelection.ZoneIDs
	}
	return nil
}

// parseUpdates reads an update body: a list of updates, one update, or a
// list wrapped in an object.
func parseUpdates(b []byte) ([]update, error) {
	var list []update
	if json.Unmarshal(b, &list) == nil {
		return list, nil
	}
	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(b, &wrapped); err != nil {
		return nil, fmt.Errorf("unreadable update: %w", err)
	}
	for _, k := range []string{"configurationUpdateData", "data", "updates"} {
		if raw, ok := wrapped[k]; ok {
			if json.Unmarshal(raw, &list) == nil {
				return list, nil
			}
			var one update
			if json.Unmarshal(raw, &one) == nil {
				return []update{one}, nil
			}
		}
	}
	var one update
	if err := json.Unmarshal(b, &one); err != nil {
		return nil, err
	}
	return []update{one}, nil
}

// apply changes a configuration the way an update says. An update that
// sets only a colour replaces the effect with that colour.
func (u *update) apply(s *Single) {
	if u.Graphics != nil {
		s.Graphics = append([]Graphic(nil), (*u.Graphics)...)
	}
	if u.Foundations != nil {
		s.Foundation = Foundation{}
		if len(*u.Foundations) > 0 {
			s.Foundation = (*u.Foundations)[0]
		}
	} else if u.DefaultColor != nil && u.Graphics == nil {
		s.Foundation = Foundation{}
		s.Graphics = []Graphic{}
	}
	if u.DefaultColor != nil {
		s.DefaultColor = *u.DefaultColor
	}
	if u.Metadata != nil {
		s.Metadata = *u.Metadata
	}
	if u.UniqueEffectType != nil {
		s.UniqueEffectType = *u.UniqueEffectType
	} else if u.Metadata != nil {
		// A new effect without a unique effect (Solid Color, Breathe...)
		// ends the old one: GG omits the field instead of sending 0.
		s.UniqueEffectType = 0
		s.UniqueEffectData = json.RawMessage(`{}`)
	}
	if len(u.UniqueEffectData) > 0 {
		s.UniqueEffectData = u.UniqueEffectData
	}
	if u.DimFactorOnMonitorSleep != nil {
		s.DimFactorOnMonitorSleep = *u.DimFactorOnMonitorSleep
	}
	if len(u.TriggerConfigs) > 0 {
		s.TriggerConfig = u.TriggerConfigs
	}
	s.normalize()
}

// setsEffect reports whether an update sets the effect, rather than only
// what goes with it (the dimming while the monitors sleep).
func (u *update) setsEffect() bool {
	return u.DefaultColor != nil || u.Foundations != nil || u.Graphics != nil || u.Metadata != nil ||
		u.UniqueEffectType != nil || len(u.UniqueEffectData) > 0 || len(u.TriggerConfigs) > 0
}

func copySingle(s *Single) Single {
	b, _ := json.Marshal(s)
	var out Single
	json.Unmarshal(b, &out)
	out.normalize()
	return out
}

// applyUpdates applies updates to one layer of a configuration, on the
// zones selected (sel, else each update's own zone selection; every
// connected zone when none is given), and saves it.
func (s *Service) applyUpdates(c *configRow, mode string, ups []update, sel []string) {
	devs := s.connected()
	all := map[string]bool{}
	for _, d := range devs {
		for _, z := range d.Zones {
			all[z.UniqueID] = true
		}
	}
	selection := func(u *update) map[string]bool {
		ids := sel
		if len(ids) == 0 && u.ZoneSelection != nil {
			ids = u.ZoneSelection.ZoneIDs
		}
		if len(ids) == 0 {
			return all
		}
		m := map[string]bool{}
		for _, id := range ids {
			if all[id] {
				m[id] = true
			}
		}
		return m
	}
	g := c.Data.layer(mode)
	for i := range ups {
		u := &ups[i]
		zones := selection(u)
		if u.PerKeySelectionLimitation != nil {
			limited := map[string]bool{}
			for _, d := range devs {
				for _, z := range d.Zones {
					if zones[z.UniqueID] && d.HasBitmapCoordinates && slices.Contains(*u.PerKeySelectionLimitation, BitmapArea{z.BitmapX, z.BitmapY}) {
						limited[z.UniqueID] = true
					}
				}
			}
			zones = limited
		}
		if len(zones) == 0 {
			continue
		}
		if len(zones) == len(all) && u.PerKeySelectionLimitation == nil {
			if u.setsEffect() {
				// Every zone: the global configuration, with no overrides left.
				g.DeviceTypeConfigs, g.PerKeyConfigs, g.DeviceConfigs = []DeviceTypeConfig{}, []PerKeyConfig{}, []DeviceConfig{}
				u.apply(&g.GlobalConfig)
				continue
			}
			// Every zone, effects kept (the dimming alone): each
			// configuration in the layer takes it.
			u.apply(&g.GlobalConfig)
			for i := range g.DeviceTypeConfigs {
				u.apply(&g.DeviceTypeConfigs[i].Config)
			}
			for i := range g.PerKeyConfigs {
				u.apply(&g.PerKeyConfigs[i].Config)
			}
			for i := range g.DeviceConfigs {
				u.apply(&g.DeviceConfigs[i].Config)
			}
			continue
		}
		for _, d := range devs {
			// The picked zones, by the configuration each shows now: what
			// the update leaves alone stays each zone's own.
			var picked []string
			var froms []*Single
			byFrom := map[*Single][]string{}
			for i := range d.Zones {
				z := &d.Zones[i]
				if zones[z.UniqueID] {
					picked = append(picked, z.UniqueID)
					from := g.resolve(d, z)
					if _, ok := byFrom[from]; !ok {
						froms = append(froms, from)
					}
					byFrom[from] = append(byFrom[from], z.UniqueID)
				}
			}
			if len(picked) == 0 {
				continue
			}
			var made []DeviceConfig
			seen := map[string]int{}
			for _, from := range froms {
				cfg := copySingle(from)
				u.apply(&cfg)
				// Zones that end up alike share one override.
				key, _ := json.Marshal(cfg)
				if i, ok := seen[string(key)]; ok {
					made[i].ZoneIDs = append(made[i].ZoneIDs, byFrom[from]...)
					continue
				}
				seen[string(key)] = len(made)
				made = append(made, DeviceConfig{DeviceID: d.ID, ZoneIDs: byFrom[from], Config: cfg})
			}
			// The zones leave the overrides they were in.
			kept := g.DeviceConfigs[:0]
			for _, dc := range g.DeviceConfigs {
				dc.ZoneIDs = slices.DeleteFunc(dc.ZoneIDs, func(id string) bool { return slices.Contains(picked, id) })
				if len(dc.ZoneIDs) > 0 {
					kept = append(kept, dc)
				}
			}
			g.DeviceConfigs = append(kept, made...)
		}
	}
	g.normalize()
	if err := s.saveConfig(c); err == nil {
		s.sendPreviews(c)
	}
}
