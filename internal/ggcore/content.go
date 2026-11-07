package ggcore

import (
	"net/http"
	"os"
	"path/filepath"

	"ggl/internal/httpx"
	"ggl/internal/paths"
)

var localeNames = []struct{ ISO, Name string }{
	{"en_US", "English"}, {"de", "Deutsch"}, {"es", "Español"}, {"fr", "Français"},
	{"it", "Italiano"}, {"ja", "日本語"}, {"ko", "한국어"}, {"pt", "Português"},
	{"pt_br", "Português (Brasil)"}, {"ru_RU", "Русский"}, {"zh_hans", "简体中文"},
	{"zh_hant", "繁體中文"}, {"zh_HK", "繁體中文 (香港)"},
}

func (c *Core) contentRoutes() {
	s := c.srv
	// Locale files come from the installer's localization/ directory and
	// already have the {"locale": {...}} shape the client expects.
	s.Handle("GET /locale", func(w http.ResponseWriter, r *http.Request) {
		loc, _ := c.Setting("locale").(string)
		b, err := os.ReadFile(filepath.Join(paths.Localization(), loc+".json"))
		if err != nil {
			b, err = os.ReadFile(filepath.Join(paths.Localization(), "en_US.json"))
		}
		if err != nil {
			httpx.Error(w, 500, "localization files missing; re-run gg-setup")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	s.Handle("GET /supported_locales", func(w http.ResponseWriter, r *http.Request) {
		var out []map[string]string
		for _, l := range localeNames {
			if _, err := os.Stat(filepath.Join(paths.Localization(), l.ISO+".json")); err == nil {
				out = append(out, map[string]string{"iso": l.ISO, "language": l.Name})
			}
		}
		httpx.JSON(w, map[string]any{"locales": out})
	})

	// Home, Engine and Giveaways pages are filled with content the Windows
	// core downloads from steelseries.com. gg-linux stays offline, so the
	// layouts are empty and the client shows its built-in pages.
	for _, p := range []string{"home", "giveaways", "engine"} {
		s.Handle("GET /"+p+"/bucketContent/layout", func(w http.ResponseWriter, r *http.Request) {
			httpx.JSON(w, map[string]any{"layout": []any{}})
		})
		s.Handle("GET /"+p+"/bucketContent/content", func(w http.ResponseWriter, r *http.Request) {
			httpx.JSON(w, map[string]any{"content": []any{}})
		})
	}
	s.Handle("GET /whatsnew/bucketContent/content", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"content": []any{}})
	})
	s.Handle("GET /promos", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"activepromos": []any{}})
	})
	s.Handle("POST /promos/code", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusServiceUnavailable, "promotions are not available offline")
	})

	// Content view history: which cards/tips the user has dismissed.
	s.Handle("GET /contentViewHistory/{id}", func(w http.ResponseWriter, r *http.Request) {
		seen := map[string]bool{}
		c.db.Get("contentViewHistory", &seen)
		httpx.JSON(w, map[string]any{"hasContentBeenViewed": seen[r.PathValue("id")]})
	})
	s.Handle("POST /contentViewHistory/{id}", func(w http.ResponseWriter, r *http.Request) {
		seen := map[string]bool{}
		c.db.Get("contentViewHistory", &seen)
		seen[r.PathValue("id")] = true
		c.db.Set("contentViewHistory", seen)
		httpx.OK(w)
	})
	s.Handle("GET /contentViewHistory", func(w http.ResponseWriter, r *http.Request) {
		seen := map[string]bool{}
		c.db.Get("contentViewHistory", &seen)
		out := map[string]bool{}
		for _, id := range r.URL.Query()["ids"] {
			out[id] = seen[id]
		}
		httpx.JSON(w, map[string]any{"hasContentBeenViewed": out})
	})
	s.Handle("POST /contentViewHistory", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			IDs []string `json:"ids"`
		}](r)
		seen := map[string]bool{}
		c.db.Get("contentViewHistory", &seen)
		for _, id := range p.IDs {
			seen[id] = true
		}
		c.db.Set("contentViewHistory", seen)
		httpx.OK(w)
	})
}
