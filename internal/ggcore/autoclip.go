package ggcore

import (
	"encoding/json"
	"net/http"

	"ggl/internal/httpx"
)

// Auto-clip rules: which GameSense game events make Moments save a clip.
// GG keeps them for Moments. Games publish their rules over GameSense;
// until one does, the lists are empty and only the user's choices are
// kept.
func (c *Core) autoclipRoutes() {
	s := c.srv
	s.Handle("GET /moments/autoclipSettings", func(w http.ResponseWriter, r *http.Request) {
		settings := []json.RawMessage{}
		c.db.Get("autoclipSettings", &settings)
		httpx.JSON(w, map[string]any{"get_autoclip_settings": settings})
	})
	s.Handle("POST /moments/autoclipSettings", func(w http.ResponseWriter, r *http.Request) {
		var p []struct {
			RuleID  string `json:"rule_id"`
			Enabled int    `json:"enabled"`
		}
		if err := httpx.Decode(r, &p); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		choices := map[string]int{}
		c.db.Get("autoclipChoices", &choices)
		for _, v := range p {
			choices[v.RuleID] = v.Enabled
		}
		c.db.Set("autoclipChoices", choices)
		httpx.OK(w)
	})
	s.Handle("GET /moments/autoclipSettings/options/{id}", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"get_autoclip_settings_options": map[string]any{
			"game_name": r.PathValue("id"), "display_name": "", "helper_text": "", "rules": []any{},
		}})
	})
	s.Handle("POST /moments/autoclipSettings/options", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
}
