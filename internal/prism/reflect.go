package prism

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"os/exec"
	"sync"
	"time"

	"ggl/internal/wlcopy"
)

// The Reflect effect (unique effect 1), as Windows Prism draws it: each
// zone takes the colour of a part of the screen. The screen is scaled to
// 180 pixels high; a zone's part is a rectangle of it from its sampling
// area (Left, Right, Top and Bottom are thirds of the screen, the
// corners quarters, All the whole of it), and on a device with a key
// grid the Top, Bottom and All areas split into a column per key column.
// The colour is the rectangle's average, saturated (S^0.05 in HSV), and
// eased over 0.1 s.
//
// The screen comes from the compositor through wlr-screencopy
// (Hyprland, sway...): the focused monitor, copied when it changes, up to
// 10 times a second, while a configuration uses the effect.

const effectReflect = 1

// Sampling areas (rendering_service_types.SamplingArea).
const (
	areaDefault     = 1
	areaLeft        = 2
	areaRight       = 3
	areaTop         = 4
	areaBottom      = 5
	areaAll         = 6
	areaTopLeft     = 7
	areaTopRight    = 8
	areaBottomLeft  = 9
	areaBottomRight = 10
)

// rect is a relative rectangle of the screen (processing.RelativeRectangle).
type rect struct{ minX, minY, maxX, maxY float64 }

// areaRect is regionFromArea.
func areaRect(area int) rect {
	switch area {
	case areaLeft:
		return rect{0, 0, 0.33, 1}
	case areaRight:
		return rect{0.67, 0, 1, 1}
	case areaTop:
		return rect{0, 0, 1, 0.33}
	case areaBottom:
		return rect{0, 0.67, 1, 1}
	case areaAll:
		return rect{0, 0, 1, 1}
	case areaTopLeft:
		return rect{0, 0, 0.5, 0.5}
	case areaTopRight:
		return rect{0.5, 0, 1, 0.5}
	case areaBottomLeft:
		return rect{0, 0.5, 0.5, 1}
	case areaBottomRight:
		return rect{0.5, 0.5, 1, 1}
	}
	return rect{}
}

// defaultArea is GetDefaultSamplingAreaMap: keyboards sample the bottom
// of the screen, mice its bottom right corner, headsets and microphones
// its top, speakers (type 11) its left or right side, the rest all of it.
func defaultArea(z *zone) int {
	switch z.DeviceType {
	case 0, 7, 10:
		return areaBottom
	case 1:
		return areaBottomRight
	case 3, 12:
		return areaTop
	case 11:
		if z.X == 0 {
			return areaLeft
		}
		return areaRight
	}
	return areaAll
}

// zoneRect is regionFromZoneAndArea: a key grid's column of the Top,
// Bottom and All areas, else the area.
func zoneRect(z *zone, area int) rect {
	if area == areaDefault || area == 0 {
		area = defaultArea(z)
	}
	r := areaRect(area)
	if z.DeviceHasBitmapCoordinates && z.DeviceBitmapXSize != 0 && area >= areaTop && area <= areaAll {
		w := (r.maxX - r.minX) / float64(z.DeviceBitmapXSize)
		x := float64(uint8(z.BitmapX - z.DeviceBitmapXMin))
		r.minX, r.maxX = x*w, (x+1)*w
	}
	return r
}

// samplingArea reads a configuration's sampling area (GG sends its
// number; its name is taken too).
func samplingArea(s *Single) int {
	var d struct {
		ReflectData *struct {
			SamplingArea any `json:"samplingArea"`
		} `json:"reflectData"`
	}
	if json.Unmarshal(s.UniqueEffectData, &d) == nil && d.ReflectData != nil {
		switch v := d.ReflectData.SamplingArea.(type) {
		case float64:
			return int(v)
		case string:
			if n, ok := areaNames[v]; ok {
				return n
			}
		}
	}
	return areaDefault
}

// areaNames are the sampling areas' names in the client's other enum.
var areaNames = map[string]int{
	"SAMPLING_AREA_APPLYDEFAULT": areaDefault, "SAMPLING_AREA_LEFT": areaLeft, "SAMPLING_AREA_RIGHT": areaRight,
	"SAMPLING_AREA_TOP": areaTop, "SAMPLING_AREA_BOTTOM": areaBottom, "SAMPLING_AREA_ALL": areaAll,
	"SAMPLING_AREA_TOP_LEFT": areaTopLeft, "SAMPLING_AREA_TOP_RIGHT": areaTopRight,
	"SAMPLING_AREA_BOTTOM_LEFT": areaBottomLeft, "SAMPLING_AREA_BOTTOM_RIGHT": areaBottomRight,
}

// screen is the screen scaled to 180 pixels high, RGB 0-1.
type screen struct {
	w, h int
	pix  []float64 // r, g, b per pixel
}

const screenHeight = 180

// average is the colour of a relative rectangle of the screen, as
// processRegions and process take it.
func (s *screen) average(r rect) [3]float64 {
	x0, y0 := int(r.minX*float64(s.w)), int(r.minY*float64(s.h))
	x1, y1 := int(r.maxX*float64(s.w)), int(r.maxY*float64(s.h))
	x1, y1 = max(min(x1, s.w), x0+1), max(min(y1, s.h), y0+1)
	x0, y0 = min(max(x0, 0), s.w-1), min(max(y0, 0), s.h-1)
	var sum [3]float64
	n := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			p := s.pix[(y*s.w+x)*3:]
			sum[0] += p[0]
			sum[1] += p[1]
			sum[2] += p[2]
			n++
		}
	}
	if n == 0 {
		return sum
	}
	return [3]float64{sum[0] / float64(n), sum[1] / float64(n), sum[2] / float64(n)}
}

// saturate is process's colour boost: saturation to the power 0.05.
func saturate(c [3]float64) [3]float64 {
	h, s, v := rgbToHSV(c[0], c[1], c[2])
	r, g, b := hsvToRGB(h, math.Pow(s, 0.05), v)
	return [3]float64{r, g, b}
}

func rgbToHSV(r, g, b float64) (h, s, v float64) {
	mx, mn := max(r, g, b), min(r, g, b)
	v = mx
	d := mx - mn
	if mx > 0 {
		s = d / mx
	}
	if d == 0 {
		return 0, s, v
	}
	switch mx {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h, s, v
}

func hsvToRGB(h, s, v float64) (r, g, b float64) {
	c := v * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := v - c
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return r + m, g + m, b + m
}

// reflectTau is the smoothing time constant (REFLECT_TIME_SMOOTH).
const reflectTau = 0.1

// reflectFrame turns the screen into zone colours. Each rectangle's
// colour eases towards the screen's on every lighting frame, so it moves
// smoothly between the (fewer) captures.
type reflectFrame struct {
	scr     *screen
	now     time.Time
	smooth  map[rect]smoothed // shared across frames; nil for previews
	colours map[rect]Color
}

type smoothed struct {
	scr    *screen    // the screen target was taken from
	target [3]float64 // its colour there
	c      [3]float64 // the colour shown
	at     time.Time
}

func (f *reflectFrame) color(z *zone, s *Single) Color {
	r := zoneRect(z, samplingArea(s))
	if c, ok := f.colours[r]; ok {
		return c
	}
	var c [3]float64
	switch {
	case f.scr == nil:
	case f.smooth == nil:
		c = saturate(f.scr.average(r))
	default:
		prev, ok := f.smooth[r]
		target := prev.target
		if !ok || prev.scr != f.scr {
			target = saturate(f.scr.average(r))
		}
		c = target
		if ok {
			k := math.Exp(-f.now.Sub(prev.at).Seconds() / reflectTau)
			for i := range c {
				c[i] = target[i]*(1-k) + prev.c[i]*k
			}
		}
		f.smooth[r] = smoothed{f.scr, target, c, f.now}
	}
	col := Color{uint8(c[0]*255 + 0.5), uint8(c[1]*255 + 0.5), uint8(c[2]*255 + 0.5)}
	f.colours[r] = col
	return col
}

// usesReflect reports whether a layer shows the Reflect effect.
func usesReflect(g *Group) bool {
	if g.GlobalConfig.UniqueEffectType == effectReflect {
		return true
	}
	for i := range g.DeviceTypeConfigs {
		if g.DeviceTypeConfigs[i].Config.UniqueEffectType == effectReflect {
			return true
		}
	}
	for i := range g.PerKeyConfigs {
		if g.PerKeyConfigs[i].Config.UniqueEffectType == effectReflect {
			return true
		}
	}
	for i := range g.DeviceConfigs {
		if g.DeviceConfigs[i].Config.UniqueEffectType == effectReflect {
			return true
		}
	}
	return false
}

// screenCapture captures the focused monitor while Reflect is used.
type screenCapture struct {
	mu     sync.Mutex
	on     bool
	stop   chan struct{}
	latest *screen
	smooth map[rect]smoothed
	warned bool
}

// need starts or stops capturing.
func (sc *screenCapture) need(on bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if on == sc.on {
		return
	}
	sc.on = on
	if !on {
		close(sc.stop)
		sc.latest, sc.smooth = nil, nil
		return
	}
	sc.stop = make(chan struct{})
	sc.smooth = map[rect]smoothed{}
	go sc.run(sc.stop)
}

// frame is the latest screen for rendering.
func (sc *screenCapture) frame() *reflectFrame {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return &reflectFrame{scr: sc.latest, now: time.Now(), smooth: sc.smooth, colours: map[rect]Color{}}
}

func (sc *screenCapture) run(stop chan struct{}) {
	for {
		err := sc.capture(stop)
		if err == nil {
			slog.Info("prism: Reflect stopped capturing")
			return
		}
		// The compositor went away, or a monitor did: connect again, which
		// also finds monitors plugged in since.
		sc.mu.Lock()
		if !sc.warned {
			slog.Warn("prism: Reflect can't capture the screen", "err", err)
			sc.warned = true
		}
		sc.mu.Unlock()
		select {
		case <-stop:
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// capture copies the screen until stop (returning nil) or an error.
func (sc *screenCapture) capture(stop chan struct{}) error {
	c, err := wlcopy.Open()
	if err != nil {
		return err
	}
	defer c.Close()
	sc.mu.Lock()
	if !sc.warned {
		slog.Info("prism: Reflect capturing the screen")
	}
	sc.mu.Unlock()
	// Copying costs the compositor a read back of the monitor: on Hyprland
	// at 2560x1440 a few percent of a core for any copying, about 10% at
	// 10 copies a second. So the monitor is copied only when it changes,
	// and at most 10 times a second.
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	output, picked := "", time.Time{}
	for {
		select {
		case <-stop:
			return nil
		case <-tick.C:
		}
		if time.Since(picked) > 2*time.Second {
			output, picked = focusedOutput(c), time.Now()
		}
		f, err := c.CaptureChanged(output)
		if errors.Is(err, wlcopy.ErrUnchanged) {
			continue
		}
		if err != nil {
			return err
		}
		scr := downscale(f)
		sc.mu.Lock()
		if sc.stop == stop {
			sc.latest = scr
		}
		sc.warned = false
		sc.mu.Unlock()
	}
}

// focusedOutput is the monitor in use: Hyprland's focused one, else the
// first.
func focusedOutput(c *wlcopy.Client) string {
	outs := c.Outputs()
	if b, err := exec.Command("hyprctl", "-j", "monitors").Output(); err == nil {
		var mons []struct {
			Name    string `json:"name"`
			Focused bool   `json:"focused"`
		}
		if json.Unmarshal(b, &mons) == nil {
			for _, m := range mons {
				if m.Focused {
					return m.Name
				}
			}
		}
	}
	if len(outs) > 0 {
		return outs[0].Name
	}
	return ""
}

// downscale is quickScale: the frame scaled to 180 pixels high, each pixel
// the mean of four samples of its block. Hyprland copies a rotated
// monitor the way it is shown, so the frame's transform is left alone.
func downscale(f *wlcopy.Frame) *screen {
	h := screenHeight
	w := max(int(math.Round(float64(f.Width)*float64(h)/float64(f.Height))), 1)
	s := &screen{w: w, h: h, pix: make([]float64, w*h*3)}
	bgr := f.Format == wlcopy.FormatARGB8888 || f.Format == wlcopy.FormatXRGB8888
	bx, by := float64(f.Width)/float64(w), float64(f.Height)/float64(h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, b float64
			for _, o := range [4][2]float64{{0.25, 0.25}, {0.75, 0.25}, {0.25, 0.75}, {0.75, 0.75}} {
				sx := min(int((float64(x)+o[0])*bx), f.Width-1)
				sy := min(int((float64(y)+o[1])*by), f.Height-1)
				if f.YInvert {
					sy = f.Height - 1 - sy
				}
				p := f.Pix[sy*f.Stride+sx*4:]
				if bgr {
					b, g, r = b+float64(p[0]), g+float64(p[1]), r+float64(p[2])
				} else {
					r, g, b = r+float64(p[0]), g+float64(p[1]), b+float64(p[2])
				}
			}
			i := (y*w + x) * 3
			s.pix[i], s.pix[i+1], s.pix[i+2] = r/1020, g/1020, b/1020
		}
	}
	return s
}

// previewScreen is a made-up screen for GG's previews of the effect, as
// Windows Prism shows a sample picture in them: a sunset sky over a
// dark sea.
func previewScreen() *screen {
	w, h := 320, screenHeight
	s := &screen{w: w, h: h, pix: make([]float64, w*h*3)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx, fy := float64(x)/float64(w-1), float64(y)/float64(h-1)
			var c [3]float64
			if fy < 0.6 {
				hue := 20 + 230*fx
				c[0], c[1], c[2] = hsvToRGB(hue, 0.8, 0.9-0.3*fy)
			} else {
				c[0], c[1], c[2] = hsvToRGB(200, 0.6, 0.25)
			}
			copy(s.pix[(y*w+x)*3:], c[:])
		}
	}
	return s
}
