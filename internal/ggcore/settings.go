package ggcore

import (
	"net/http"
	"os"
	"strings"
	"time"

	"ggl/internal/httpx"
)

// Application settings registered by setting.NewAppSettings in the GG
// core, with the same defaults.
var settingDefaults = map[string]any{
	"personalizationEnabled":        true,
	"locale":                        "",
	"betaUpdatesEnabled":            false,
	"shouldAutoStart":               true,
	"overlayEnabled":                true,
	"loadoutsEligible":              false,
	"autoDeployLoadouts":            true,
	"autoApplyQuicksetPresets":      false,
	"autoApplyQuicksetMousePresets": false,
	"autoApplyQuicksetSonarPresets": true,
	"quicksetProfilesEnabled":       false,
	"quicksetBlinkingButton":        false,
	"autoFlattenEQEnabled":          true,
	"advancedAudioFirstViewDate":    "",
	"enableAutomaticGameScanning":   true,
	"deviceOwner":                   "",
	"audioLiteEnabled":              true,
}

func (c *Core) settings() map[string]any {
	out := map[string]any{}
	for k, v := range settingDefaults {
		out[k] = v
	}
	stored := map[string]any{}
	c.db.Get("settings", &stored)
	for k, v := range stored {
		out[k] = v
	}
	if out["locale"] == "" {
		out["locale"] = systemLocale()
	}
	return out
}

// Setting returns one setting's current value.
func (c *Core) Setting(key string) any { return c.settings()[key] }

func (c *Core) setSetting(key string, value any) {
	stored := map[string]any{}
	c.db.Get("settings", &stored)
	stored[key] = value
	c.db.Set("settings", stored)
	c.Broadcast("EVENT_APP_SETTING_CHANGED", map[string]any{"key": key, "value": value})
	if key == "locale" {
		c.Broadcast("EVENT_LOCALE_CHANGED", map[string]any{"locale": value})
	}
}

func (c *Core) settingsRoutes() {
	s := c.srv
	s.Handle("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"settings": c.settings()})
	})
	s.Handle("POST /settings", func(w http.ResponseWriter, r *http.Request) {
		p, err := decode[struct {
			Key   string `json:"key"`
			Value any    `json:"value"`
		}](r)
		if err != nil || p.Key == "" {
			httpx.Error(w, 400, "bad setting")
			return
		}
		c.setSetting(p.Key, p.Value)
		httpx.JSON(w, map[string]any{"settings": c.settings()})
	})
	s.Handle("POST /settings/dateTime", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}](r)
		if p.Value == "" {
			p.Value = time.Now().UTC().Format(time.RFC3339)
		}
		c.setSetting(p.Key, p.Value)
		httpx.JSON(w, map[string]any{"settings": c.settings()})
	})
}

// systemLocale maps $LANG to one of the client's locale files.
func systemLocale() string {
	lang := os.Getenv("LC_ALL")
	if lang == "" {
		lang = os.Getenv("LANG")
	}
	lang = strings.SplitN(lang, ".", 2)[0]
	switch {
	case strings.HasPrefix(lang, "pt_BR"):
		return "pt_br"
	case strings.HasPrefix(lang, "zh_TW"):
		return "zh_hant"
	case strings.HasPrefix(lang, "zh_HK"):
		return "zh_HK"
	case strings.HasPrefix(lang, "zh"):
		return "zh_hans"
	case strings.HasPrefix(lang, "ru"):
		return "ru_RU"
	}
	for _, l := range []string{"de", "es", "fr", "it", "ja", "ko", "pt"} {
		if strings.HasPrefix(lang, l) {
			return l
		}
	}
	return "en_US"
}
