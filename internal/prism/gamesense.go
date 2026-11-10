package prism

import (
	"ggl/internal/engine"
	"ggl/internal/gamesense"
)

// GameSense lighting: games light keys and zones through the GameSense
// SDK (see package gamesense), and Prism shows their colours over its own
// on the zones they use, as GG merges the two on Windows. When the games
// stop, Prism's lighting is back.

// gsDevice is a Prism device as GameSense sees it, with the GameSense
// code (HID code or zone number) of each of its zones.
type gsDevice struct {
	dev   gamesense.Device
	codes []int // by zone, -1 for none
	remap map[int]int
}

func newGSDevice(e engine.PrismDevice, d *device) *gsDevice {
	info := e.GameSense
	if info == nil || len(d.Zones) == 0 {
		return nil
	}
	g := &gsDevice{dev: gamesense.Device{ProductID: e.ProductID, Types: info.Types, Zones: info.Zones}, remap: info.Remap}
	perKey := false
	for _, t := range info.Types {
		perKey = perKey || t == "rgb-per-key-zones"
	}
	g.codes = make([]int, len(d.Zones))
	if perKey {
		g.dev.Grid = make([]int, 22*6)
		for i := range g.dev.Grid {
			g.dev.Grid[i] = -1
		}
		for i, z := range d.Zones {
			g.codes[i] = -1
			if z.HidCode == 0 {
				continue
			}
			g.codes[i] = int(z.HidCode)
			if d.HasBitmapCoordinates && z.BitmapX < 22 && z.BitmapY < 6 {
				g.dev.Grid[int(z.BitmapY)*22+int(z.BitmapX)] = int(z.HidCode)
			}
		}
		return g
	}
	// A zoned device's frame names each zone by its number in the byte
	// before its colour, the number GameSense uses; else the zones go in
	// order.
	seen := map[int]bool{}
	numbered := true
	for i, z := range d.Zones {
		w := d.writes[z.src.Write].Data
		if z.src.Red < 1 || z.src.Red > len(w) {
			numbered = false
			break
		}
		n := int(w[z.src.Red-1])
		if n >= len(d.Zones) || seen[n] {
			numbered = false
			break
		}
		seen[n] = true
		g.codes[i] = n
	}
	if !numbered {
		for i := range g.codes {
			g.codes[i] = i
		}
	}
	return g
}

// drawGameSense puts the active games' colours on the devices' zones.
func drawGameSense(gs *gamesense.State, colors map[string]Color, devs []*device) {
	if gs == nil || !gs.Active() {
		return
	}
	for _, d := range devs {
		if d.gs == nil {
			continue
		}
		lights := gs.Lights(&d.gs.dev)
		if len(lights) == 0 {
			continue
		}
		for code, to := range d.gs.remap {
			if c, ok := lights[code]; ok {
				lights[to] = c
			}
		}
		for i := range d.Zones {
			if c, ok := lights[d.gs.codes[i]]; ok && d.gs.codes[i] >= 0 {
				colors[d.Zones[i].UniqueID] = Color{c.R, c.G, c.B}
			}
		}
	}
}
