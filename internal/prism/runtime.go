package prism

import (
	"bytes"
	"context"
	"log/slog"
	"sort"
	"time"

	"ggl/internal/devicert"
)

// refreshDevices reads the connected devices PrismSync can light and
// remembers new ones (enabled unless the user switched them off).
func (s *Service) refreshDevices() {
	if s.eng == nil {
		return
	}
	left := s.leftHanded()
	now := map[int]*device{}
	for _, e := range s.eng.PrismSyncDevices() {
		var adj *[2]int32
		var x, y int32
		if s.db.QueryRow(`SELECT x_adjustment, y_adjustment FROM device_coordinate_adjustments WHERE device_id = ?`, e.ID).Scan(&x, &y) == nil {
			adj = &[2]int32{x, y}
		}
		d := newDevice(e, adj, left)
		var enabled, first int
		err := s.db.QueryRow(`SELECT enabled, first_connection FROM device_cache WHERE device_id = ?`, e.ID).Scan(&enabled, &first)
		if err != nil {
			// First seen: lit by PrismSync, as on Windows.
			s.db.Exec(`INSERT INTO device_cache (device_id, has_bitmap_coordinates, device_type, name, full_name, region, enabled, first_connection)
				VALUES (?, ?, ?, ?, ?, ?, 1, 1)`, e.ID, d.HasBitmapCoordinates, d.Type, d.Name, d.FullName, d.Region)
			enabled, first = 1, 1
			slog.Info("prism: new device", "device", d.FullName, "zones", len(d.Zones))
		}
		d.Enabled, d.FirstTimeConnection = enabled != 0, first != 0
		now[d.ID] = d
	}
	s.mu.Lock()
	var added, removed []int
	for id := range now {
		if _, ok := s.devices[id]; !ok {
			added = append(added, id)
		}
	}
	for id := range s.devices {
		if _, ok := now[id]; !ok {
			removed = append(removed, id)
		}
	}
	s.devices = now
	for k := range s.previews {
		delete(s.previews, k)
	}
	s.mu.Unlock()
	for _, id := range added {
		s.notify("EVENT_PRISMSYNC_DEVICE_CONNECTED", map[string]any{"id": id})
	}
	for _, id := range removed {
		s.notify("EVENT_PRISMSYNC_DEVICE_DISCONNECTED", map[string]any{"id": id})
	}
	s.reconcile()
}

// connected lists the connected devices, by id.
func (s *Service) connected() []*device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*device, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Service) reconcile() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// renderZones renders the base layer of a configuration at time t for
// the devices given.
// (Audio Visualizer zones show made-up sound, as in GG's previews.)
func renderZones(c *ConfigData, devs []*device, t int64) map[string]Color {
	var av *avFrame
	if usesVisualizer(&c.Base) {
		av = previewFrame(gridWidths(devs), t)
	}
	var rf *reflectFrame
	if usesReflect(&c.Base) {
		rf = &reflectFrame{scr: previewScreen(), colours: map[rect]Color{}}
	}
	return renderZonesAsleep(c, devs, t, false, av, rf)
}

// renderZonesAsleep renders, dimmed as each configuration says when the
// monitors are asleep; Audio Visualizer zones show av, Reflect zones rf.
func renderZonesAsleep(c *ConfigData, devs []*device, t int64, asleep bool, av *avFrame, rf *reflectFrame) map[string]Color {
	out := make(map[string]Color, 128)
	ready := map[*Single]*compiled{}
	for _, d := range devs {
		for i := range d.Zones {
			z := &d.Zones[i]
			cfg := c.Base.resolve(d, z)
			r, ok := ready[cfg]
			if !ok {
				r = compile(cfg, waveOrigin(cfg, c, devs))
				ready[cfg] = r
			}
			var col Color
			switch {
			case cfg.UniqueEffectType == effectAudioVisualizer:
				if av == nil {
					av = &avFrame{}
				}
				col = avColor(av, z, profileOf(cfg))
			case cfg.UniqueEffectType == effectReflect:
				if rf == nil {
					rf = &reflectFrame{colours: map[rect]Color{}}
				}
				col = rf.color(z, cfg)
			default:
				col = r.color(z.pos(), t)
			}
			if asleep && r.dim > 0 {
				col = scale(col, 255-r.dim)
			}
			out[z.UniqueID] = col
		}
	}
	return out
}

// waveOrigin is where a configuration's foundation wave starts: its fixed
// origin, else the middle of the zones it lights for radial waves (the
// origin only shifts other waves).
func waveOrigin(cfg *Single, c *ConfigData, devs []*device) point {
	g := cfg.graphic(cfg.Foundation.GraphicID)
	if g == nil {
		return point{}
	}
	o := g.Foreground.Origin
	if o.X != 0 || o.Y != 0 || g.Foreground.PhaseMatrixShape != shapeRadial {
		return point{o.X, o.Y}
	}
	minX, minY, maxX, maxY := 1<<30, 1<<30, -1, -1
	for _, d := range devs {
		for i := range d.Zones {
			z := &d.Zones[i]
			if c.Base.resolve(d, z) != cfg {
				continue
			}
			p := z.pos()
			minX, minY, maxX, maxY = min(minX, p.X), min(minY, p.Y), max(maxX, p.X), max(maxY, p.Y)
		}
	}
	if maxX < 0 {
		return point{}
	}
	return point{(minX + maxX) / 2, (minY + maxY) / 2}
}

// renderLoop renders the current configuration every step and hands each
// enabled device its frame. Each device has its own writer, so a slow one
// (a wireless mouse) never holds up the others.
func (s *Service) renderLoop(ctx context.Context) {
	tick := time.NewTicker(33 * time.Millisecond)
	defer tick.Stop()
	writers := map[int]*frameWriter{}
	defer func() {
		for _, w := range writers {
			w.stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			s.audio.need(false, "")
			s.screen.need(false)
			s.releaseAll()
			return
		case <-tick.C:
		case <-s.kick:
		}
		// Prism lights the devices enabled in its tab with the current
		// configuration, GameSense over it. Without a configuration it
		// lights a device only while a game (or ImageSync) shows something
		// on it.
		gs := s.eng.GameSense()
		gsActive := gs != nil && gs.Active()
		s.mu.Lock()
		if s.current == nil {
			if c := s.currentConfig(); c != nil {
				s.current = c
				s.started = time.Now()
			}
		}
		cur, started, lastInput := s.current, s.started, s.lastInput
		lights := func(d *device) bool {
			return d.Enabled && (cur != nil || (gsActive && d.gs != nil && len(gs.Lights(&d.gs.dev)) > 0))
		}
		var active []*device
		present := map[int]bool{}
		for _, d := range s.devices {
			present[d.ID] = true
			if lights(d) {
				active = append(active, d)
			}
		}
		var release []int
		for id := range s.lit {
			if d, ok := s.devices[id]; !ok || !lights(d) {
				release = append(release, id)
				delete(s.lit, id)
			}
		}
		s.mu.Unlock()
		for _, id := range release {
			if w := writers[id]; w != nil {
				w.stop()
				delete(writers, id)
			}
			slog.Info("prism: giving the device its own lighting back", "device", id)
			go s.eng.PrismRelease(id)
		}
		if len(active) == 0 {
			s.watchInput(false)
			s.audio.need(false, "")
			s.screen.need(false)
			continue
		}
		name := "GameSense"
		if cur == nil {
			s.watchInput(false)
			s.audio.need(false, "")
			s.screen.need(false)
			colors := map[string]Color{}
			drawGameSense(gs, colors, active)
			s.writeFrames(writers, active, colors, name)
			s.dropGone(writers, present)
			continue
		}
		name = cur.Name
		s.watchInput(usesInput(&cur.Data))
		layer := ConfigData{Base: *s.layerFor(&cur.Data, lastInput)}
		asleep := usesDimming(&layer.Base) && monitorsAsleep()
		var av *avFrame
		if usesVisualizer(&layer.Base) {
			scalar, output := s.visualizerSettings()
			s.audio.need(true, output)
			av = s.audio.frame(gridWidths(active), scalar)
		} else {
			s.audio.need(false, "")
		}
		var rf *reflectFrame
		if usesReflect(&layer.Base) {
			s.screen.need(true)
			rf = s.screen.frame()
		} else {
			s.screen.need(false)
		}
		colors := renderZonesAsleep(&layer, active, time.Since(started).Milliseconds(), asleep, av, rf)
		s.react(colors, active)
		drawGameSense(gs, colors, active)
		s.writeFrames(writers, active, colors, name)
		s.dropGone(writers, present)
	}
}

// writeFrames hands each lit device its frame.
func (s *Service) writeFrames(writers map[int]*frameWriter, active []*device, colors map[string]Color, config string) {
	for _, d := range active {
		w := writers[d.ID]
		if w == nil || w.dev != d {
			if w != nil {
				w.stop()
			}
			s.mu.Lock()
			first := !s.lit[d.ID]
			s.lit[d.ID] = true
			s.mu.Unlock()
			w = newFrameWriter(s.eng, d, first, config)
			writers[d.ID] = w
		}
		w.offer(d.frame(colors))
	}
}

// dropGone stops the writers of devices that went away.
func (s *Service) dropGone(writers map[int]*frameWriter, present map[int]bool) {
	for id, w := range writers {
		if !present[id] {
			w.stop()
			delete(writers, id)
		}
	}
}

// frameWriter sends one device the newest frame it was offered, skipping
// unchanged frames (but refreshing every couple of seconds).
type frameWriter struct {
	dev  *device
	next chan []devicert.PrismWrite
	quit chan struct{}
}

func newFrameWriter(eng Engine, d *device, first bool, config string) *frameWriter {
	w := &frameWriter{dev: d, next: make(chan []devicert.PrismWrite, 1), quit: make(chan struct{})}
	go func() {
		if first && len(d.init) > 0 {
			if err := eng.PrismWrite(d.ID, d.init); err != nil {
				slog.Warn("prism: init", "device", d.FullName, "err", err)
			}
		}
		var last []byte
		var sent time.Time
		logged := !first
		failed := false
		var backoff time.Duration
		var retry time.Time
		for {
			var f []devicert.PrismWrite
			select {
			case <-w.quit:
				return
			case f = <-w.next:
			}
			flat := flatten(f)
			if bytes.Equal(flat, last) && time.Since(sent) < 2*time.Second {
				continue
			}
			if time.Now().Before(retry) {
				continue
			}
			if err := eng.PrismWrite(d.ID, f); err != nil {
				if !failed {
					slog.Warn("prism: frame not written", "device", d.FullName, "err", err)
					failed = true
				}
				// A device not answering (a wireless mouse asleep) is
				// left alone for a while rather than waited on per frame.
				backoff = min(max(backoff*2, 2*time.Second), 15*time.Second)
				retry = time.Now().Add(backoff)
				continue
			}
			if failed {
				slog.Info("prism: device takes frames again", "device", d.FullName)
			}
			failed, backoff = false, 0
			last, sent = flat, time.Now()
			if !logged {
				slog.Info("prism: lighting device", "device", d.FullName, "config", config)
				logged = true
			}
		}
	}()
	return w
}

// offer replaces any frame still waiting with f.
func (w *frameWriter) offer(f []devicert.PrismWrite) {
	select {
	case <-w.next:
	default:
	}
	select {
	case w.next <- f:
	default:
	}
}

func (w *frameWriter) stop() {
	select {
	case <-w.quit:
	default:
		close(w.quit)
	}
}

func flatten(ws []devicert.PrismWrite) []byte {
	var b []byte
	for _, w := range ws {
		b = append(b, w.Data...)
	}
	return b
}

// releaseAll gives every lit device its own lighting back (when ggd stops).
func (s *Service) releaseAll() {
	s.mu.Lock()
	ids := make([]int, 0, len(s.lit))
	for id := range s.lit {
		ids = append(ids, id)
	}
	s.lit = map[int]bool{}
	s.mu.Unlock()
	for _, id := range ids {
		s.eng.PrismRelease(id)
	}
}

// preview is configuration_types.ConfigPreviewData (frames flattened in,
// as the Windows JSON has them).
type preview struct {
	ConfigID   string             `json:"configId"`
	Mode       string             `json:"mode"`
	IsUpToDate bool               `json:"isUpToDate"`
	FrameCount int                `json:"frameCount"`
	Frames     []map[string]Color `json:"frames"`
	DelayMs    int                `json:"delayMs"`
}

// Previews are 48 frames 125 ms apart, as on Windows; a still
// configuration needs one.
const (
	previewFrames  = 48
	previewDelayMs = 125
)

func (s *Service) preview(c *configRow, mode string, devs []*device) preview {
	key := c.ID + "/" + mode
	s.mu.Lock()
	p, ok := s.previews[key]
	s.mu.Unlock()
	if ok {
		return p
	}
	g := c.Data.layer(mode)
	p = preview{ConfigID: c.ID, Mode: mode, IsUpToDate: true, DelayMs: previewDelayMs, Frames: []map[string]Color{}}
	data := ConfigData{Base: *g}
	n := 1
	if animated(g) {
		n = previewFrames
	}
	for i := 0; i < n; i++ {
		p.Frames = append(p.Frames, renderZones(&data, devs, int64(i*previewDelayMs)))
	}
	p.FrameCount = len(p.Frames)
	s.mu.Lock()
	s.previews[key] = p
	s.mu.Unlock()
	return p
}

// animated reports whether a layer has a moving graphic.
func animated(g *Group) bool {
	if usesVisualizer(g) || usesReflect(g) {
		return true
	}
	moving := func(s *Single) bool {
		gr := s.graphic(s.Foundation.GraphicID)
		return gr != nil && (gr.HasForeground && gr.Foreground.Duration > 0 && len(gr.Foreground.Gradient.Colors) > 1 || gr.HasAperture)
	}
	if moving(&g.GlobalConfig) {
		return true
	}
	for i := range g.DeviceTypeConfigs {
		if moving(&g.DeviceTypeConfigs[i].Config) {
			return true
		}
	}
	for i := range g.PerKeyConfigs {
		if moving(&g.PerKeyConfigs[i].Config) {
			return true
		}
	}
	for i := range g.DeviceConfigs {
		if moving(&g.DeviceConfigs[i].Config) {
			return true
		}
	}
	return false
}
