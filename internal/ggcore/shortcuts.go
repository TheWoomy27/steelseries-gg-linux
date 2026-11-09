package ggcore

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"ggl/internal/httpx"
	"ggl/internal/paths"
)

// Keyboard shortcuts: GG's database migrations hold the shortcuts and
// their defaults (keyboard_shortcuts; Alt+S saves a Moments clip, and so
// on). Those migrations can't be replayed whole (they assume tables from
// GG versions the installer no longer carries), so only the shortcut
// statements are, into memory; the user's changes are kept in GG's state.

// Shortcut is inputlib.KeyboardShortcut.
type Shortcut struct {
	ID             int    `json:"id"`
	TriggerData    string `json:"trigger_data"`
	DefaultTrigger string `json:"default_trigger"`
}

// Trigger is a shortcut's key: a HID usage on page 1 (the keyboard) with
// a HID modifier mask (1 Ctrl, 2 Shift, 4 Alt, 8 GUI; right-hand ones
// shifted by 4).
type Trigger struct {
	HIDCode   int `json:"hidCode"`
	Page      int `json:"page"`
	Type      int `json:"type"`
	Direction int `json:"direction"`
	Modifiers int `json:"modifiers"`
}

// Shortcut ids the Moments shortcuts use.
const (
	ShortcutMomentsClip    = 0
	ShortcutMomentsMicMute = 1
	ShortcutMomentsDesktop = 2
	ShortcutMomentsEnable  = 3
)

type shortcutStore struct {
	once     sync.Once
	defaults []Shortcut
	mu       sync.Mutex
	changed  []func()
}

// shortcutDefaults replays the keyboard_shortcuts statements of GG's
// migrations, in order.
func shortcutDefaults(dir string) ([]Shortcut, error) {
	files, _ := filepath.Glob(filepath.Join(dir, "*", "*.sql"))
	sort.Slice(files, func(i, j int) bool { return filepath.Base(files[i]) < filepath.Base(files[j]) })
	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	d.SetMaxOpenConns(1)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		up, _, _ := strings.Cut(string(b), "-- +goose Down")
		for _, st := range strings.Split(up, ";") {
			if !strings.Contains(st, "keyboard_shortcuts") {
				continue
			}
			if _, err := d.Exec(st); err != nil {
				return nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
			}
		}
	}
	rows, err := d.Query(`SELECT id, trigger_data, default_trigger FROM keyboard_shortcuts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Shortcut
	for rows.Next() {
		var s Shortcut
		if err := rows.Scan(&s.ID, &s.TriggerData, &s.DefaultTrigger); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Shortcuts lists the keyboard shortcuts.
func (c *Core) Shortcuts() ([]Shortcut, error) {
	st := &c.shortcuts
	st.once.Do(func() {
		var err error
		if st.defaults, err = shortcutDefaults(paths.GGDBMig()); err != nil {
			slog.Error("keyboard shortcuts", "err", err)
		}
	})
	set := map[string]string{}
	c.db.Get("shortcutTriggers", &set)
	out := make([]Shortcut, 0, len(st.defaults))
	for _, s := range st.defaults {
		if t, ok := set[strconv.Itoa(s.ID)]; ok {
			s.TriggerData = t
		}
		out = append(out, s)
	}
	return out, nil
}

// ShortcutTrigger is the key a shortcut is set to; ok is false when it
// has none.
func (c *Core) ShortcutTrigger(id int) (Trigger, bool) {
	all, err := c.Shortcuts()
	if err != nil {
		return Trigger{}, false
	}
	for _, s := range all {
		if s.ID != id {
			continue
		}
		var t Trigger
		if json.Unmarshal([]byte(s.TriggerData), &t) != nil || t.HIDCode == 0 {
			return Trigger{}, false
		}
		return t, true
	}
	return Trigger{}, false
}

// OnShortcutsChanged runs fn after the user changes a shortcut.
func (c *Core) OnShortcutsChanged(fn func()) {
	c.shortcuts.mu.Lock()
	c.shortcuts.changed = append(c.shortcuts.changed, fn)
	c.shortcuts.mu.Unlock()
}

func (c *Core) shortcutRoutes() {
	s := c.srv
	s.Handle("GET /keyboardShortcuts", func(w http.ResponseWriter, r *http.Request) {
		all, err := c.Shortcuts()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.JSON(w, map[string]any{"keyboardShortcuts": all})
	})
	s.Handle("POST /keyboardShortcuts", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			ID          *int    `json:"id"`
			TriggerData *string `json:"trigger_data"`
		}
		if err := httpx.Decode(r, &p); err != nil || p.ID == nil {
			httpx.Error(w, http.StatusBadRequest, "id required")
			return
		}
		set := map[string]string{}
		c.db.Get("shortcutTriggers", &set)
		// No trigger puts the shortcut back to its default.
		if p.TriggerData == nil {
			delete(set, strconv.Itoa(*p.ID))
		} else {
			set[strconv.Itoa(*p.ID)] = *p.TriggerData
		}
		if err := c.db.Set("shortcutTriggers", set); err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		c.shortcuts.mu.Lock()
		fns := c.shortcuts.changed
		c.shortcuts.mu.Unlock()
		for _, fn := range fns {
			go fn()
		}
		all, _ := c.Shortcuts()
		httpx.JSON(w, map[string]any{"keyboardShortcuts": all})
	})
}
