package prism

import (
	"encoding/json"
	"slices"
)

// A PrismSync configuration (configuration_types.ConfigurationData): three
// layers (base, idle after a while without input, reactive to key
// presses), each a global configuration with overrides by device type,
// by key (bitmap area) and by device zone.

type Origin struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type Gradient struct {
	Colors []ColorPosition `json:"colors"`
}

type GradientWave struct {
	Duration          int      `json:"duration"`
	SpatialWavelength int      `json:"spatialWavelength"`
	Repetitions       int      `json:"repetitions"`
	Origin            Origin   `json:"origin"`
	IsOriginFixed     bool     `json:"isOriginFixed"`
	Gradient          Gradient `json:"gradient"`
	PhaseMatrixShape  uint8    `json:"phaseMatrixShape"`
}

type ApertureSection struct {
	FinalMask Mask  `json:"finalMask"`
	Length    uint8 `json:"length"`
}

type ApertureGradient struct {
	InitialMask Mask              `json:"initialMask"`
	Sections    []ApertureSection `json:"sections"`
}

type ApertureWave struct {
	Duration          int              `json:"duration"`
	SpatialWavelength int              `json:"spatialWavelength"`
	Repetitions       int              `json:"repetitions"`
	Origin            Origin           `json:"origin"`
	IsOriginFixed     bool             `json:"isOriginFixed"`
	Gradient          ApertureGradient `json:"gradient"`
	PhaseMatrixShape  uint8            `json:"phaseMatrixShape"`
}

type Graphic struct {
	Type          uint8        `json:"type"`
	ZIndex        int          `json:"zIndex"`
	UniqueID      string       `json:"uniqueId"`
	HasForeground bool         `json:"hasForeground"`
	Foreground    GradientWave `json:"foreground"`
	HasBackground bool         `json:"hasBackground"`
	Background    GradientWave `json:"background"`
	HasAperture   bool         `json:"hasAperture"`
	Aperture      ApertureWave `json:"aperture"`
}

type Foundation struct {
	Origin    Origin `json:"origin"`
	GraphicID string `json:"graphicId"`
	Opacity   uint8  `json:"opacity"`
}

// Single is configuration_types.SingleConfiguration.
type Single struct {
	Graphics                []Graphic       `json:"graphics"`
	DefaultColor            Color           `json:"defaultColor"`
	Foundation              Foundation      `json:"foundation"`
	TriggerConfig           json.RawMessage `json:"triggerConfig,omitempty"`
	Metadata                map[string]any  `json:"metadata"`
	UniqueEffectType        int             `json:"uniqueEffectType"`
	UniqueEffectData        json.RawMessage `json:"uniqueEffectData,omitempty"`
	DimFactorOnMonitorSleep uint8           `json:"dimFactorOnMonitorSleep"`
}

func (s *Single) graphic(id string) *Graphic {
	for i := range s.Graphics {
		if s.Graphics[i].UniqueID == id {
			return &s.Graphics[i]
		}
	}
	return nil
}

func (s *Single) normalize() {
	if s.Graphics == nil {
		s.Graphics = []Graphic{}
	}
	if s.Metadata == nil {
		s.Metadata = map[string]any{}
	}
}

type BitmapArea struct {
	X uint8 `json:"x"`
	Y uint8 `json:"y"`
}

type DeviceTypeConfig struct {
	DeviceType int    `json:"deviceType"`
	Config     Single `json:"config"`
}

type PerKeyConfig struct {
	BitmapAreas []BitmapArea `json:"bitmapAreas"`
	Config      Single       `json:"config"`
}

type DeviceConfig struct {
	DeviceID int      `json:"deviceId"`
	ZoneIDs  []string `json:"zoneIds"`
	Config   Single   `json:"config"`
}

// Group is configuration_types.ConfigurationGroup: one layer.
type Group struct {
	GlobalConfig      Single             `json:"globalConfig"`
	DeviceTypeConfigs []DeviceTypeConfig `json:"deviceTypeConfigs"`
	PerKeyConfigs     []PerKeyConfig     `json:"perKeyConfigs"`
	DeviceConfigs     []DeviceConfig     `json:"deviceConfigs"`
}

func (g *Group) normalize() {
	g.GlobalConfig.normalize()
	if g.DeviceTypeConfigs == nil {
		g.DeviceTypeConfigs = []DeviceTypeConfig{}
	}
	if g.PerKeyConfigs == nil {
		g.PerKeyConfigs = []PerKeyConfig{}
	}
	if g.DeviceConfigs == nil {
		g.DeviceConfigs = []DeviceConfig{}
	}
	for i := range g.DeviceTypeConfigs {
		g.DeviceTypeConfigs[i].Config.normalize()
	}
	for i := range g.PerKeyConfigs {
		g.PerKeyConfigs[i].Config.normalize()
	}
	for i := range g.DeviceConfigs {
		g.DeviceConfigs[i].Config.normalize()
		if g.DeviceConfigs[i].ZoneIDs == nil {
			g.DeviceConfigs[i].ZoneIDs = []string{}
		}
	}
}

// resolve finds the configuration lighting a zone: the zone's own, then
// its key's, then its device type's, then the global one.
func (g *Group) resolve(d *device, z *zone) *Single {
	for i := range g.DeviceConfigs {
		dc := &g.DeviceConfigs[i]
		if dc.DeviceID == d.ID && slices.Contains(dc.ZoneIDs, z.UniqueID) {
			return &dc.Config
		}
	}
	if d.HasBitmapCoordinates {
		for i := range g.PerKeyConfigs {
			for _, a := range g.PerKeyConfigs[i].BitmapAreas {
				if a.X == z.BitmapX && a.Y == z.BitmapY {
					return &g.PerKeyConfigs[i].Config
				}
			}
		}
	}
	for i := range g.DeviceTypeConfigs {
		if g.DeviceTypeConfigs[i].DeviceType == d.Type {
			return &g.DeviceTypeConfigs[i].Config
		}
	}
	return &g.GlobalConfig
}

// ConfigData is configuration_types.ConfigurationData.
type ConfigData struct {
	Base       Group `json:"base"`
	Idle       Group `json:"idle"`
	IdleTimeMs int   `json:"idleTimeMs"`
	Reactive   Group `json:"reactive"`
}

func (c *ConfigData) normalize() {
	c.Base.normalize()
	c.Idle.normalize()
	c.Reactive.normalize()
}

func (c *ConfigData) layer(mode string) *Group {
	switch mode {
	case modeIdle:
		return &c.Idle
	case modeReactive:
		return &c.Reactive
	}
	return &c.Base
}

// Layers ("modes") of a configuration.
const (
	modeBase     = "base"
	modeIdle     = "idle"
	modeReactive = "reactive"
)

func parseConfig(raw string) ConfigData {
	var c ConfigData
	json.Unmarshal([]byte(raw), &c)
	c.normalize()
	return c
}

// zoneConfigData is configuration_types.ZoneConfigData: what the client
// shows for a zone.
type zoneConfigData struct {
	BaseFoundationGraphicIDs    []string        `json:"baseFoundationGraphicIds"`
	IdleFoundationGraphicIDs    []string        `json:"idleFoundationGraphicIds"`
	ReactiveGraphicIDs          []string        `json:"reactiveGraphicIds"`
	BaseDefaultColor            Color           `json:"baseDefaultColor"`
	IdleDefaultColor            Color           `json:"idleDefaultColor"`
	TriggerConfig               json.RawMessage `json:"triggerConfig"`
	BaseMetadata                map[string]any  `json:"baseMetadata"`
	ReactiveMetadata            map[string]any  `json:"reactiveMetadata"`
	IdleMetadata                map[string]any  `json:"idleMetadata"`
	BaseUniqueEffectType        int             `json:"baseUniqueEffectType"`
	BaseUniqueEffectData        json.RawMessage `json:"baseUniqueEffectData"`
	IdleUniqueEffectType        int             `json:"idleUniqueEffectType"`
	IdleUniqueEffectData        json.RawMessage `json:"idleUniqueEffectData"`
	BaseDimFactorOnMonitorSleep uint8           `json:"baseDimFactorOnMonitorSleep"`
	IdleDimFactorOnMonitorSleep uint8           `json:"idleDimFactorOnMonitorSleep"`
}

func ids(gid string) []string {
	if gid == "" {
		return []string{}
	}
	return []string{gid}
}

func rawOr(r json.RawMessage, def string) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage(def)
	}
	return r
}

// frontendZones is the per-zone view of a configuration for the devices
// present, with every graphic it uses.
func frontendZones(c *ConfigData, devs []*device) (map[string]zoneConfigData, map[string]Graphic) {
	zones := map[string]zoneConfigData{}
	graphics := map[string]Graphic{}
	add := func(s *Single) {
		for _, g := range s.Graphics {
			graphics[g.UniqueID] = g
		}
	}
	for _, d := range devs {
		for i := range d.Zones {
			z := &d.Zones[i]
			b, id, r := c.Base.resolve(d, z), c.Idle.resolve(d, z), c.Reactive.resolve(d, z)
			add(b)
			add(id)
			add(r)
			rg := []string{}
			for _, g := range r.Graphics {
				rg = append(rg, g.UniqueID)
			}
			zones[z.UniqueID] = zoneConfigData{
				BaseFoundationGraphicIDs: ids(b.Foundation.GraphicID), IdleFoundationGraphicIDs: ids(id.Foundation.GraphicID),
				ReactiveGraphicIDs: rg, BaseDefaultColor: b.DefaultColor, IdleDefaultColor: id.DefaultColor,
				TriggerConfig: rawOr(r.TriggerConfig, "null"), BaseMetadata: b.Metadata, ReactiveMetadata: r.Metadata, IdleMetadata: id.Metadata,
				BaseUniqueEffectType: b.UniqueEffectType, BaseUniqueEffectData: rawOr(b.UniqueEffectData, "{}"),
				IdleUniqueEffectType: id.UniqueEffectType, IdleUniqueEffectData: rawOr(id.UniqueEffectData, "{}"),
				BaseDimFactorOnMonitorSleep: b.DimFactorOnMonitorSleep, IdleDimFactorOnMonitorSleep: id.DimFactorOnMonitorSleep,
			}
		}
	}
	return zones, graphics
}
