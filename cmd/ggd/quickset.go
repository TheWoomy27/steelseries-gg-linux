package main

import (
	"ggl/internal/ggcore"
	"ggl/internal/sonar"
)

// quicksetBridge connects Sonar to GG's QuickSet profiles in the GG core;
// the two packages keep their own types.
type quicksetBridge struct{ core *ggcore.Core }

func (b quicksetBridge) QuicksetStatus() (active, profilesEnabled bool) {
	return b.core.QuicksetStatus()
}

func (b quicksetBridge) QuicksetProfiles() ([]sonar.QuicksetProfile, string) {
	loadouts, active := b.core.Loadouts()
	out := make([]sonar.QuicksetProfile, 0, len(loadouts))
	for _, l := range loadouts {
		p := sonar.QuicksetProfile{ID: l.ID, Name: l.Name, Configs: sonarConfigs(l.Configs)}
		for _, bd := range l.Bindings {
			switch bd.Type {
			case "game":
				p.GameUID = bd.Value
			case "custom":
				p.CustomID = bd.Value
			}
		}
		out = append(out, p)
	}
	return out, active
}

func (b quicksetBridge) GameImage(uid string) string { return b.core.GameImage(uid) }

func (b quicksetBridge) AddDefaultConfig(c sonar.QuicksetConfig) {
	b.core.AddDefaultConfig(loadoutConfig(c))
}

func (b quicksetBridge) SetActiveConfig(c sonar.QuicksetConfig) {
	b.core.SetActiveConfig(loadoutConfig(c))
}

func (b quicksetBridge) ReplaceConfig(typ, oldID, newID string) {
	b.core.ReplaceConfig(typ, oldID, newID)
}

func (b quicksetBridge) CachePresets(app string, presets []sonar.QuicksetPreset) {
	out := make([]ggcore.CachedPreset, 0, len(presets))
	for _, p := range presets {
		out = append(out, ggcore.CachedPreset{ID: p.ID, Name: p.Name, PresetType: app, GameIDs: p.GameIDs})
	}
	b.core.CachePresets(app, out)
}

func loadoutConfig(c sonar.QuicksetConfig) ggcore.LoadoutConfig {
	return ggcore.LoadoutConfig{ConfigType: c.Type, ConfigMetadata: c.Metadata, ConfigID: c.ID}
}

func sonarConfigs(cfgs []ggcore.LoadoutConfig) []sonar.QuicksetConfig {
	out := make([]sonar.QuicksetConfig, 0, len(cfgs))
	for _, c := range cfgs {
		out = append(out, sonar.QuicksetConfig{ID: c.ConfigID, Metadata: c.ConfigMetadata, Type: c.ConfigType})
	}
	return out
}

// connectQuickset wires Sonar and the GG core's QuickSet both ways.
func connectQuickset(core *ggcore.Core, snr *sonar.Service) {
	snr.SetQuickset(quicksetBridge{core})
	core.OnLoadoutChange(func(cfgs []ggcore.LoadoutConfig) { snr.ApplyQuickset(sonarConfigs(cfgs)) })
	core.OnQuicksetStatus(snr.QuicksetStatusChanged)
	core.OnLoadoutsModified(snr.QuicksetModified)
}
