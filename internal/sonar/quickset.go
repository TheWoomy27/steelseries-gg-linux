package sonar

import (
	"net/http"
	"sort"
	"strings"

	"ggl/internal/httpx"
)

// QuickSet, GG's game profiles, as Sonar's QuicksetService takes part in
// them: Sonar tells GG its Game preset outside games and the games each
// Game preset suits; GG holds a Game preset in each profile and has Sonar
// select it when it deploys the profile; picking a Game preset changes the
// active profile's. The client shows the profiles beside Sonar's presets
// (/quickset). Only the Game channel takes part, as in Sonar.

// QuicksetConfig is a configuration in a GG profile.
type QuicksetConfig struct {
	ID, Metadata, Type string
}

// QuicksetProfile is a GG profile as Sonar shows it.
type QuicksetProfile struct {
	ID, Name string
	GameUID  string // the game it is bound to, if any
	CustomID string // its custom binding, if any
	Configs  []QuicksetConfig
}

// QuicksetPreset is a Game preset with the games it suits.
type QuicksetPreset struct {
	ID, Name string
	GameIDs  []string
}

// QuicksetHost is GG's side of QuickSet.
type QuicksetHost interface {
	QuicksetStatus() (active, profilesEnabled bool)
	QuicksetProfiles() (profiles []QuicksetProfile, activeID string)
	GameImage(uid string) string
	AddDefaultConfig(c QuicksetConfig)
	SetActiveConfig(c QuicksetConfig)
	ReplaceConfig(typ, oldID, newID string)
	CachePresets(app string, presets []QuicksetPreset)
}

// quicksetType is Sonar's configuration type in GG profiles.
const quicksetType = "sonar"

// SetQuickset connects Sonar to GG's QuickSet.
func (s *Service) SetQuickset(h QuicksetHost) {
	s.mu.Lock()
	s.quickset = h
	s.mu.Unlock()
}

// quicksetHost is GG's QuickSet while it is available.
func (s *Service) quicksetHost() QuicksetHost {
	s.mu.Lock()
	h := s.quickset
	s.mu.Unlock()
	if h == nil {
		return nil
	}
	if active, _ := h.QuicksetStatus(); !active {
		return nil
	}
	return h
}

// quicksetProfile is Sonar's Profile.
type quicksetProfile struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	IsGame        bool    `json:"isGame"`
	GameImagePath *string `json:"gameImagePath"`
	IsCustom      bool    `json:"isCustom"`
	CustomID      string  `json:"customId"`
	SonarConfigID *string `json:"sonarConfigId"`
}

func (s *Service) quicksetEnabled() bool {
	h := s.quicksetHost()
	if h == nil {
		return false
	}
	_, on := h.QuicksetStatus()
	return on
}

// quicksetProfiles lists GG's profiles by name, with the active one.
func (s *Service) quicksetProfiles() ([]quicksetProfile, *quicksetProfile) {
	out := []quicksetProfile{}
	h := s.quicksetHost()
	if h == nil {
		return out, nil
	}
	profiles, activeID := h.QuicksetProfiles()
	var active *quicksetProfile
	for _, p := range profiles {
		q := quicksetProfile{ID: p.ID, Name: p.Name, IsGame: p.GameUID != "", IsCustom: p.CustomID != "", CustomID: p.CustomID}
		if q.IsGame {
			img := h.GameImage(p.GameUID)
			q.GameImagePath = &img
		}
		for _, c := range p.Configs {
			if c.Type == quicksetType {
				id := c.ID
				q.SonarConfigID = &id
			}
		}
		out = append(out, q)
		if p.ID == activeID {
			a := q
			active = &a
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, active
}

// registerQuickset tells GG the Game preset Sonar uses outside games and
// the games each Game preset suits (RegisterDefaultConfigToGG and
// SendConfigAssociation).
func (s *Service) registerQuickset() {
	h := s.quicksetHost()
	if h == nil {
		return
	}
	if selected, err := s.queryConfigs(`WHERE id IN (SELECT config_id FROM selected_config WHERE vad = ?)`, vadGame); err == nil && len(selected) > 0 {
		h.AddDefaultConfig(QuicksetConfig{ID: selected[0].ID, Type: quicksetType})
	}
	cfgs, err := s.queryConfigs(`WHERE vad = ?`, vadGame)
	if err != nil {
		return
	}
	presets := make([]QuicksetPreset, 0, len(cfgs))
	for _, c := range cfgs {
		p := QuicksetPreset{ID: c.ID, Name: c.Name, GameIDs: []string{}}
		for _, id := range strings.Split(c.GameIDs, ",") {
			if id = strings.TrimSpace(id); id != "" {
				p.GameIDs = append(p.GameIDs, id)
			}
		}
		presets = append(presets, p)
	}
	h.CachePresets(quicksetType, presets)
}

// quicksetSelected follows a Game preset the user picked: the active
// profile uses it from now on.
func (s *Service) quicksetSelected(c *config) {
	if c.VAD != vadGame {
		return
	}
	if h := s.quicksetHost(); h != nil {
		h.SetActiveConfig(QuicksetConfig{ID: c.ID, Type: quicksetType})
		s.sendQuicksetProfiles()
	}
}

// quicksetDeleted points the profiles using a deleted Game preset at the
// default one.
func (s *Service) quicksetDeleted(c *config) {
	if c.VAD != vadGame {
		return
	}
	if h := s.quicksetHost(); h != nil {
		h.ReplaceConfig(quicksetType, c.ID, defaultPresetIDs[vadGame])
	}
}

// ApplyQuickset selects the Game preset of a profile GG deployed.
func (s *Service) ApplyQuickset(configs []QuicksetConfig) {
	for _, q := range configs {
		if q.Type != quicksetType || q.ID == "" {
			continue
		}
		cur, err := s.queryConfigs(`WHERE id IN (SELECT config_id FROM selected_config WHERE vad = ?)`, vadGame)
		if err == nil && len(cur) > 0 && strings.EqualFold(cur[0].ID, q.ID) {
			break
		}
		if c, err := s.selectConfig(q.ID); err == nil && c.VAD == vadGame {
			s.ws.Broadcast("SONAR_EVENT_SELECTED_CONFIG_UPDATED", c)
		}
		break
	}
	s.sendQuicksetProfiles()
}

// QuicksetStatusChanged follows QuickSet's profiles being turned on or off.
func (s *Service) QuicksetStatusChanged() {
	if s.isActive() {
		s.registerQuickset()
	}
	s.ws.Broadcast("SONAR_EVENT_QUICKSET_IS_ENABLED_UPDATED", s.quicksetEnabled())
	s.sendQuicksetProfiles()
}

// QuicksetModified follows profiles being added, renamed or changed.
func (s *Service) QuicksetModified() { s.sendQuicksetProfiles() }

func (s *Service) sendQuicksetProfiles() {
	profiles, selected := s.quicksetProfiles()
	s.ws.Broadcast("SONAR_EVENT_QUICKSET_PROFILES_UPDATED", profiles)
	s.ws.Broadcast("SONAR_EVENT_QUICKSET_SELECTED_PROFILE_UPDATED", selected)
}

func (s *Service) quicksetRoutes() {
	sv := s.srv
	sv.Handle("GET /quickset/isEnabled", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, s.quicksetEnabled()) })
	sv.Handle("GET /quickset/selected", func(w http.ResponseWriter, r *http.Request) {
		_, selected := s.quicksetProfiles()
		httpx.JSON(w, selected)
	})
	sv.Handle("GET /quickset/profiles", func(w http.ResponseWriter, r *http.Request) {
		profiles, _ := s.quicksetProfiles()
		httpx.JSON(w, profiles)
	})
}
