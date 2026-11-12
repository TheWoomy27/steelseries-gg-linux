package ggcore

import (
	"net/http"

	"ggl/internal/httpx"
)

// Overlays are the in-game pop-ups Windows GG draws for its products
// (QuickSet, the Engine, Moments), each switched in that product's
// "Overlays and Notifications" settings. gg-linux shows desktop
// notifications in their place, which follow the same switches. Windows
// keeps them in the core's overlay_products and overlays tables, filled by
// each product when it registers; the rows below are those the settings
// pages list.

type overlayDef struct {
	name         string
	localization string
	enabled      bool // on at first
}

type overlayProductDef struct {
	id       int
	name     string
	overlays []overlayDef
}

var overlayProducts = []overlayProductDef{
	{1, "quickset", []overlayDef{
		{"loadoutSwitchedAuto", "loadouts.settings.overlaysNotifications.loadoutSwitchedAuto.title", true},
		{"loadoutSwitchedManual", "loadouts.settings.overlaysNotifications.loadoutSwitchedManual.title", false},
		{"loadoutSwitchedShortcut", "loadouts.settings.overlaysNotifications.loadoutSwitchedShortcut.title", true},
	}},
	{2, "engine", []overlayDef{
		{"lowBattery", "engine.settings.overlaysNotifications.batteryOverlays.lowBattery.title", true},
	}},
	{3, "Moments", []overlayDef{
		{"clipCaptured", "moments.settings.overlaysNotifications.gameOverlay.overlays.clipCaptured", true},
		{"momentsActive", "moments.settings.overlaysNotifications.gameOverlay.overlays.momentsActive", true},
		{"desktopModeActivated", "moments.settings.overlaysNotifications.gameOverlay.overlays.desktopModeActivated", true},
		{"desktopModeDeactivated", "moments.settings.overlaysNotifications.gameOverlay.overlays.desktopModeDeactivated", true},
	}},
}

// overlayState is what the user changed: a product or overlay switched,
// or a product's overlays moved.
type overlayState struct {
	Products map[string]bool `json:"products"` // by product name
	Overlays map[string]bool `json:"overlays"` // by "product/overlay"
	Position map[string]int  `json:"position"` // by product name
}

func (c *Core) overlayState() overlayState {
	st := overlayState{}
	c.db.Get("overlays", &st)
	if st.Products == nil {
		st.Products = map[string]bool{}
	}
	if st.Overlays == nil {
		st.Overlays = map[string]bool{}
	}
	if st.Position == nil {
		st.Position = map[string]int{}
	}
	return st
}

// overlayRow and overlayProductRow are the Windows core's rows, which it
// sends without JSON names.
type overlayRow struct {
	ProductId       int
	Name            string
	Localization    string
	DefaultEnabled  bool
	IsEnabled       bool
	Type            int
	GGVersion       string
	Duration        int
	DefaultPosition int
	Position        int
	SoundFilename   string
}

type overlayProductRow struct {
	Id        int
	Name      string
	IsEnabled bool
}

func (c *Core) overlaySettings() map[string]any {
	st := c.overlayState()
	settings := map[string][]overlayRow{}
	products := []overlayProductRow{}
	for _, p := range overlayProducts {
		on, set := st.Products[p.name]
		products = append(products, overlayProductRow{Id: p.id, Name: p.name, IsEnabled: on || !set})
		rows := []overlayRow{}
		for _, o := range p.overlays {
			enabled, set := st.Overlays[p.name+"/"+o.name]
			if !set {
				enabled = o.enabled
			}
			rows = append(rows, overlayRow{
				ProductId: p.id, Name: o.name, Localization: o.localization,
				DefaultEnabled: o.enabled, IsEnabled: enabled, Duration: 5, Position: st.Position[p.name],
			})
		}
		settings[p.name] = rows
	}
	return map[string]any{"settings": settings, "productsettings": products}
}

// OverlayEnabled reports whether a product's overlay is switched on, the
// product's own switch included.
func (c *Core) OverlayEnabled(product, name string) bool {
	st := c.overlayState()
	if on, set := st.Products[product]; set && !on {
		return false
	}
	if on, set := st.Overlays[product+"/"+name]; set {
		return on
	}
	for _, p := range overlayProducts {
		if p.name != product {
			continue
		}
		for _, o := range p.overlays {
			if o.name == name {
				return o.enabled
			}
		}
	}
	return false
}

// OnNotify registers what shows a desktop notification for the core.
func (c *Core) OnNotify(fn func(tag, summary, body string)) {
	c.mu.Lock()
	c.notify = fn
	c.mu.Unlock()
}

// notifyLoadout tells the user a QuickSet profile was switched, where
// Windows GG shows its "QuickSet Profile Switched" overlay.
func (c *Core) notifyLoadout(name string, manual bool) {
	overlay := "loadoutSwitchedAuto"
	if manual {
		overlay = "loadoutSwitchedManual"
	}
	c.mu.Lock()
	fn := c.notify
	c.mu.Unlock()
	if fn != nil && c.OverlayEnabled("quickset", overlay) {
		fn("quickset", "QuickSet", "Profile: "+name)
	}
}

// setOverlay switches a product (overlay "") or one of its overlays, or
// moves the product's overlays.
func (c *Core) setOverlay(product, overlay string, enabled, position *int) {
	st := c.overlayState()
	if enabled != nil {
		if overlay == "" {
			st.Products[product] = *enabled != 0
		} else {
			st.Overlays[product+"/"+overlay] = *enabled != 0
		}
	}
	if position != nil {
		st.Position[product] = *position
	}
	c.db.Set("overlays", st)
}

func (c *Core) overlayRoutes() {
	s := c.srv
	s.Handle("GET /overlays", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"get_overlay_settings": c.overlaySettings()})
	})
	// The body switches the product or overlay (enabled: 0 or 1), or
	// moves the product's overlays (position).
	update := func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			Enabled  *int `json:"enabled"`
			Position *int `json:"position"`
		}](r)
		c.setOverlay(r.PathValue("product"), r.PathValue("overlay"), p.Enabled, p.Position)
		httpx.OK(w)
	}
	s.Handle("PUT /overlays/{product}", update)
	s.Handle("PUT /overlays/{product}/{overlay}", update)
}
