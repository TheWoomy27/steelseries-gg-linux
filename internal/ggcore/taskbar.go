package ggcore

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"ggl/internal/httpx"
	"ggl/internal/paths"
	"ggl/internal/tray"
)

// The notification area icon. On Windows the core's taskbar service shows
// it; ggd does the same through the StatusNotifierItem protocol. Its menu
// is "Open SteelSeries GG", the Engine's configs, the menus sub-apps add
// through /taskbar/setMenu, and "Exit".

// menuItem is a sub-app's menu entry (contextmenu.MenuItem).
type menuItem struct {
	UUID     string     `json:"uuid"`
	Text     string     `json:"text"`
	Style    menuStyle  `json:"style"`
	Children []menuItem `json:"children"`
}

// menuStyle's menuBitmap (a battery level icon on Windows) is not shown:
// its numbering is not known.
type menuStyle struct {
	Checkmark     bool `json:"checkmark"`
	GrayedOut     bool `json:"grayedOut"`
	ClickDisabled bool `json:"clickDisabled"`
	MenuBitmap    int  `json:"menuBitmap"`
}

type taskbar struct {
	mu        sync.Mutex
	icon      *tray.Tray
	quit      func()
	keys      []string // sub-app menus in the order they were added
	menus     map[string]menuItem
	recording bool
	shown     []tray.Item
	dirty     chan struct{}
	locales   map[string]map[string]any
}

// StartTray shows the tray icon until ctx ends. Its "Exit" item calls quit.
func (c *Core) StartTray(ctx context.Context, quit func()) {
	tb := &c.taskbar
	tb.quit = quit
	tb.dirty = make(chan struct{}, 1)
	opts := tray.Options{
		ID:         "steelseries-gg",
		Title:      "SteelSeries GG",
		Icon:       trayIcon("ok"),
		IconName:   "input-gaming",
		OnActivate: c.StartClient,
		OnClick:    c.trayClick,
		OnMenuOpen: c.refreshTray,
	}
	t, err := tray.Start(opts)
	if err != nil {
		slog.Warn("tray icon unavailable", "err", err)
		return
	}
	tb.mu.Lock()
	tb.icon = t
	tb.mu.Unlock()
	c.eng.OnTrayChange(c.trayChanged)
	c.refreshTray()

	for {
		select {
		case <-ctx.Done():
			t.Close()
			return
		case <-tb.dirty:
			c.refreshTray()
			// Device status events come in bursts.
			time.Sleep(500 * time.Millisecond)
		}
	}
}

func (c *Core) trayChanged() {
	select {
	case c.taskbar.dirty <- struct{}{}:
	default:
	}
}

// trayIcon loads the icon gg-setup extracted for a state ("ok" or
// "recording") at every size it has.
func trayIcon(state string) []image.Image {
	files, _ := filepath.Glob(filepath.Join(paths.TrayIcons(), state+"-*.png"))
	var imgs []image.Image
	for _, f := range files {
		r, err := os.Open(f)
		if err != nil {
			continue
		}
		if img, err := png.Decode(r); err == nil {
			imgs = append(imgs, img)
		}
		r.Close()
	}
	return imgs
}

func (c *Core) refreshTray() {
	items := c.trayItems()
	tb := &c.taskbar
	tb.mu.Lock()
	defer tb.mu.Unlock()
	if tb.icon == nil || reflect.DeepEqual(items, tb.shown) {
		return
	}
	tb.shown = items
	tb.icon.SetMenu(items)
}

func (c *Core) trayItems() []tray.Item {
	items := []tray.Item{{ID: "open", Label: c.text("main.taskbar.openClient", "Open SteelSeries GG")}}
	var extra []tray.Item
	if devs := c.eng.TrayDevices(); len(devs) > 0 {
		configs := tray.Item{Label: c.text("deviceConfig.taskbar.configMenuTitle", "Engine configs")}
		for _, d := range devs {
			dev := tray.Item{Label: d.Name, IconName: batteryIcon(d.Battery, d.Charging)}
			for _, cfg := range d.Configs {
				dev.Children = append(dev.Children, tray.Item{ID: "config:" + cfg.ID, Label: cfg.Name, Checked: cfg.Current})
			}
			configs.Children = append(configs.Children, dev)
		}
		extra = append(extra, configs)
	}
	tb := &c.taskbar
	tb.mu.Lock()
	for _, k := range tb.keys {
		extra = append(extra, subAppTrayItem(k, tb.menus[k]))
	}
	tb.mu.Unlock()
	if len(extra) > 0 {
		items = append(items, tray.Item{Separator: true})
		items = append(items, extra...)
	}
	return append(items, tray.Item{Separator: true}, tray.Item{ID: "exit", Label: c.text("main.taskbar.exitCore", "Exit")})
}

func subAppTrayItem(key string, m menuItem) tray.Item {
	it := tray.Item{Label: m.Text, Checked: m.Style.Checkmark, Disabled: m.Style.GrayedOut}
	if m.UUID != "" && !m.Style.ClickDisabled {
		it.ID = "menu:" + m.UUID
	}
	for _, ch := range m.Children {
		it.Children = append(it.Children, subAppTrayItem(key, ch))
	}
	return it
}

// batteryIcon names the freedesktop battery icon for a level (they come in
// steps of 10).
func batteryIcon(percent int, charging bool) string {
	if percent < 0 {
		return ""
	}
	level := min(percent, 100) / 10 * 10
	if charging {
		return fmt.Sprintf("battery-level-%d-charging-symbolic", level)
	}
	return fmt.Sprintf("battery-level-%d-symbolic", level)
}

func (c *Core) trayClick(id string) {
	switch {
	case id == "open":
		c.StartClient()
	case id == "exit":
		c.taskbar.quit()
	case strings.HasPrefix(id, "config:"):
		c.eng.DeployConfigByID(strings.TrimPrefix(id, "config:"))
		c.trayChanged()
	case strings.HasPrefix(id, "menu:"):
		c.Broadcast("EVENT_TASKBAR_MENU_ITEM_CLICK", map[string]any{"uuid": strings.TrimPrefix(id, "menu:")})
	}
}

// text looks up a string in the current locale's file by its dotted path
// under "locale".
func (c *Core) text(path, fallback string) string {
	loc, _ := c.Setting("locale").(string)
	tb := &c.taskbar
	tb.mu.Lock()
	if tb.locales == nil {
		tb.locales = map[string]map[string]any{}
	}
	strs, ok := tb.locales[loc]
	if !ok {
		var f struct {
			Locale map[string]any `json:"locale"`
		}
		if b, err := os.ReadFile(filepath.Join(paths.Localization(), loc+".json")); err == nil {
			json.Unmarshal(b, &f)
		}
		strs = f.Locale
		tb.locales[loc] = strs
	}
	tb.mu.Unlock()
	var v any = strs
	for _, k := range strings.Split(path, ".") {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

func (c *Core) taskbarRoutes() {
	s := c.srv
	tb := &c.taskbar
	s.Handle("POST /taskbar/setMenu", func(w http.ResponseWriter, r *http.Request) {
		p, err := decode[struct {
			Key  string    `json:"key"`
			Item *menuItem `json:"menuItem"`
		}](r)
		if err != nil || p.Key == "" || p.Item == nil {
			httpx.Error(w, 400, "Invalid data in taskbar/setMenu API")
			return
		}
		tb.mu.Lock()
		if tb.menus == nil {
			tb.menus = map[string]menuItem{}
		}
		if _, ok := tb.menus[p.Key]; !ok {
			tb.keys = append(tb.keys, p.Key)
		}
		tb.menus[p.Key] = *p.Item
		tb.mu.Unlock()
		c.refreshTray()
		httpx.OK(w)
	})
	s.Handle("POST /taskbar/clearMenu", func(w http.ResponseWriter, r *http.Request) {
		p, err := decode[struct {
			Key string `json:"key"`
		}](r)
		if err != nil || p.Key == "" {
			httpx.Error(w, 400, "Invalid data in taskbar/clearMenu API")
			return
		}
		tb.mu.Lock()
		delete(tb.menus, p.Key)
		for i, k := range tb.keys {
			if k == p.Key {
				tb.keys = append(tb.keys[:i], tb.keys[i+1:]...)
				break
			}
		}
		tb.mu.Unlock()
		c.refreshTray()
		httpx.OK(w)
	})
	// Hover text and the recording indicator (Moments).
	s.Handle("POST /taskbar/icon", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			HoverText   string `json:"hoverText"`
			IsRecording bool   `json:"isRecording"`
		}](r)
		tb.mu.Lock()
		t, changed := tb.icon, tb.recording != p.IsRecording
		tb.recording = p.IsRecording
		tb.mu.Unlock()
		if t != nil {
			if p.HoverText != "" {
				t.SetToolTip(p.HoverText)
			}
			if changed {
				state := "ok"
				if p.IsRecording {
					state = "recording"
				}
				t.SetIcon(trayIcon(state))
			}
		}
		httpx.OK(w)
	})
	s.Handle("POST /taskbar/notification", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
}
