package prism

import (
	"math"
	"sort"
)

// The renderer follows libsteel, the Windows PrismSync renderer:
//
//   - A graphic is a gradient wave: every zone gets a phase from its
//     position relative to the wave's origin (by the wave's shape), and
//     the wave turns phase and time into a 16-bit position on a gradient.
//     For an endless wave that is 65536 × frac(t/duration − phase/(32λ)),
//     λ being the spatial wavelength (negative λ runs the other way;
//     zero makes every zone move together).
//   - A gradient is a start colour and sections of (end colour, length
//     out of 256), interpolated linearly; colour stops are turned into
//     sections that loop back to the first colour, so waves cycle.
//   - An aperture is a second wave over a gradient of masks (opacity and
//     brightness) that shapes the foreground over the background.
//   - A configuration's foundation graphic is drawn over its default
//     colour with the foundation's opacity.

// Color is libsteel.Color.
type Color struct {
	Red   uint8 `json:"red"`
	Green uint8 `json:"green"`
	Blue  uint8 `json:"blue"`
}

func lerp8(a, b uint8, t int) uint8 { return uint8(int(a) + (int(b)-int(a))*t/255) }

func mix(a, b Color, t int) Color {
	return Color{lerp8(a.Red, b.Red, t), lerp8(a.Green, b.Green, t), lerp8(a.Blue, b.Blue, t)}
}

func scale(c Color, brightness int) Color {
	return Color{uint8(int(c.Red) * brightness / 255), uint8(int(c.Green) * brightness / 255), uint8(int(c.Blue) * brightness / 255)}
}

// Mask is libsteel.Mask.
type Mask struct {
	Opacity    uint8 `json:"opacity"`
	Brightness uint8 `json:"brightness"`
}

// Phase matrix shapes.
const (
	shapeHorizontal    = 0
	shapeRadial        = 1
	shapeHorizontalRow = 2
	shapeVertical      = 3
	shapeHelix         = 4
)

// phaseIndex is a zone's phase for a wave of the given shape, dx and dy
// being its distance from the wave's origin; -1 for none.
func phaseIndex(shape uint8, dx, dy int) int32 {
	clamp := func(v float64) int32 {
		if v > 65535 {
			v = 65535
		}
		return int32(v)
	}
	switch shape {
	case shapeHorizontal:
		return clamp(math.Abs(float64(dx)))
	case shapeRadial:
		return clamp(math.Trunc(math.Hypot(float64(dx), float64(dy))))
	case shapeHorizontalRow:
		if math.Abs(float64(dy)) >= 2 {
			return -1
		}
		return clamp(math.Abs(float64(dx)))
	case shapeVertical:
		return clamp(math.Abs(float64(dy)))
	}
	return -1
}

// waveParams is what a wave needs from its configuration.
type waveParams struct {
	Duration, Wavelength, Repetitions int
}

// waveValue is libsteel's GetWaveState + GetWaveValue: the 16-bit
// gradient position at phase p after t ms, and whether the wave covers
// that phase (finite waves only cover the stretch between their front
// and their tail).
func waveValue(w waveParams, t int64, p int32) (uint16, bool) {
	if p < 0 {
		return 0, false
	}
	var rate float64
	if w.Duration != 0 {
		rate = 65536 / float64(uint16(w.Duration))
	}
	var step int64
	lam := int64(int16(w.Wavelength))
	if lam != 0 {
		step = 0x8000000 / lam
	}
	offset := int64(float64(t)*rate) << 16
	value := uint16(uint32(offset-step*int64(p)) >> 16)
	if w.Repetitions == 0 {
		return value, true
	}
	// A finite wave: its front has moved v·t/256 phase units.
	v := int64(0.125 * float64(lam) * rate)
	front := v * t / 256
	reps := int64(uint16(w.Repetitions))
	if v == 0 {
		return value, t < int64(uint16(w.Duration))
	}
	if v > 0 {
		start, end := front, front-reps*(lam<<5)
		return value, int64(p) <= start && int64(p) >= end
	}
	start := -(front + 0x10000)
	if start > 0 {
		start = 0
	}
	end := reps*(lam<<5) - front - 0x10000
	return value, int64(p) >= -start && int64(p) <= -end
}

// ColorPosition is a gradient colour stop.
type ColorPosition struct {
	Color    Color `json:"color"`
	Position uint8 `json:"position"`
}

type section struct {
	color  Color
	length int
}

// gradient is libsteel's memory gradient.
type gradient struct {
	initial  Color
	sections []section
}

// newGradient is transformGradientForLibsteel: stops sorted by position,
// closed back to the first colour at 255.
func newGradient(stops []ColorPosition) gradient {
	if len(stops) == 0 {
		return gradient{}
	}
	s := append([]ColorPosition(nil), stops...)
	sort.SliceStable(s, func(i, j int) bool { return s[i].Position < s[j].Position })
	if len(s) > 1 && s[len(s)-1].Position < 255 {
		s = append(s, ColorPosition{Color: s[0].Color, Position: 255})
	}
	g := gradient{initial: s[0].Color}
	prev := 0
	for _, c := range s {
		if c.Position == 0 {
			continue
		}
		g.sections = append(g.sections, section{c.Color, int(c.Position) - prev})
		prev = int(c.Position)
	}
	return g
}

// at is InterpolateMemoryGradient_Linear.
func (g gradient) at(v uint16) Color {
	if len(g.sections) == 0 {
		return g.initial
	}
	from, start := g.initial, 0
	for _, s := range g.sections {
		end := start + s.length<<8
		if int(v) < end {
			if s.length == 0 {
				return s.color
			}
			return mix(from, s.color, (int(v)-start)/s.length)
		}
		from, start = s.color, end
	}
	return g.sections[len(g.sections)-1].color
}

// maskGradient is a gradient of masks, for apertures.
type maskGradient struct {
	initial  Mask
	sections []struct {
		mask   Mask
		length int
	}
}

func (g maskGradient) at(v uint16) Mask {
	from, start := g.initial, 0
	for _, s := range g.sections {
		end := start + s.length<<8
		if int(v) < end {
			if s.length == 0 {
				return s.mask
			}
			t := (int(v) - start) / s.length
			return Mask{lerp8(from.Opacity, s.mask.Opacity, t), lerp8(from.Brightness, s.mask.Brightness, t)}
		}
		from, start = s.mask, end
	}
	if n := len(g.sections); n > 0 {
		return g.sections[n-1].mask
	}
	return g.initial
}

// point is a position on the PrismSync coordinate plane.
type point struct{ X, Y int }

// compiled is a configuration ready to render: its foundation graphic's
// gradients and waves worked out once, then rendered at many zones.
type compiled struct {
	def           Color
	opacity       int
	origin        point
	graphic       bool
	hasFG, hasBG  bool
	hasAperture   bool
	fg, bg        gradient
	mask          maskGradient
	fgW, bgW, apW waveParams
	fgS, bgS, apS uint8
}

func compile(s *Single, origin point) *compiled {
	c := &compiled{def: s.DefaultColor, origin: origin}
	if s.Foundation.GraphicID == "" {
		return c
	}
	g := s.graphic(s.Foundation.GraphicID)
	if g == nil {
		return c
	}
	c.graphic, c.opacity = true, int(s.Foundation.Opacity)
	if g.HasForeground {
		w := g.Foreground
		c.hasFG, c.fg, c.fgW, c.fgS = true, newGradient(w.Gradient.Colors), waveParams{w.Duration, w.SpatialWavelength, w.Repetitions}, w.PhaseMatrixShape
	}
	if g.HasBackground {
		w := g.Background
		c.hasBG, c.bg, c.bgW, c.bgS = true, newGradient(w.Gradient.Colors), waveParams{w.Duration, w.SpatialWavelength, w.Repetitions}, w.PhaseMatrixShape
	}
	if g.HasAperture {
		a := g.Aperture
		c.hasAperture, c.apW, c.apS = true, waveParams{a.Duration, a.SpatialWavelength, a.Repetitions}, a.PhaseMatrixShape
		c.mask = maskGradient{initial: a.Gradient.InitialMask}
		for _, sec := range a.Gradient.Sections {
			c.mask.sections = append(c.mask.sections, struct {
				mask   Mask
				length int
			}{sec.FinalMask, int(sec.Length)})
		}
	}
	return c
}

// color renders the configuration at a zone: the foundation graphic (its
// foreground shaped by its aperture over its background) drawn over the
// default colour with the foundation's opacity.
func (c *compiled) color(zone point, t int64) Color {
	if !c.graphic || !c.hasFG {
		return c.def
	}
	dx, dy := zone.X-c.origin.X, zone.Y-c.origin.Y
	v, in := waveValue(c.fgW, t, phaseIndex(c.fgS, dx, dy))
	if !in {
		return c.def
	}
	gc, op := c.fg.at(v), 255
	if c.hasAperture {
		av, ain := waveValue(c.apW, t, phaseIndex(c.apS, dx, dy))
		if !ain {
			return c.def
		}
		m := c.mask.at(av)
		gc = scale(gc, int(m.Brightness))
		op = int(m.Opacity)
		if c.hasBG {
			if bv, bin := waveValue(c.bgW, t, phaseIndex(c.bgS, dx, dy)); bin {
				gc, op = mix(c.bg.at(bv), gc, op), 255
			}
		}
	}
	return mix(c.def, gc, op*c.opacity/255)
}
