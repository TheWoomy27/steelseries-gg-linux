// Package oled draws what GameSense shows on a keyboard's (or headset
// base's) monochrome screen: lines of text, progress bars and the event
// icons, laid out as GG lays them out, and packs the picture the way the
// device takes it.
package oled

import (
	"image"
	"image/color"
	"image/draw"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Canvas is a monochrome picture: true pixels are lit.
type Canvas struct {
	W, H int
	px   []bool
}

// New makes a dark canvas.
func New(w, h int) *Canvas { return &Canvas{W: w, H: h, px: make([]bool, w*h)} }

// Set lights a pixel (outside the canvas is ignored).
func (c *Canvas) Set(x, y int, on bool) {
	if x >= 0 && y >= 0 && x < c.W && y < c.H {
		c.px[y*c.W+x] = on
	}
}

// At reports whether a pixel is lit.
func (c *Canvas) At(x, y int) bool {
	return x >= 0 && y >= 0 && x < c.W && y < c.H && c.px[y*c.W+x]
}

// Pack is the picture as the Apex keyboards take it: rows top to bottom,
// eight pixels a byte, the leftmost in the high bit.
func (c *Canvas) Pack() []byte {
	out := make([]byte, (c.W*c.H+7)/8)
	for i, on := range c.px {
		if on {
			out[i>>3] |= 1 << (7 - uint(i&7))
		}
	}
	return out
}

// PackVertical is the layout of the 2022 Apex screens: each byte is a
// column of eight pixels, the top one in the low bit.
func (c *Canvas) PackVertical() []byte {
	out := make([]byte, (c.W*c.H+7)/8)
	for y := 0; y < c.H; y++ {
		for x := 0; x < c.W; x++ {
			if c.px[y*c.W+x] {
				out[x+(y/8)*c.W] |= 1 << uint(y%8)
			}
		}
	}
	return out
}

// Unpack reads a picture packed as Pack packs it (GameSense image data).
func Unpack(w, h int, b []byte) *Canvas {
	c := New(w, h)
	for i := range c.px {
		if i>>3 < len(b) {
			c.px[i] = b[i>>3]&(1<<(7-uint(i&7))) != 0
		}
	}
	return c
}

var (
	facesMu sync.Mutex
	faces   = map[[2]int]font.Face{}
)

// face is the Go font at a pixel size, bold or not.
func face(size int, bold bool) font.Face {
	key := [2]int{size, 0}
	if bold {
		key[1] = 1
	}
	facesMu.Lock()
	defer facesMu.Unlock()
	if f, ok := faces[key]; ok {
		return f
	}
	data := goregular.TTF
	if bold {
		data = gobold.TTF
	}
	ft, err := opentype.Parse(data)
	if err != nil {
		panic(err)
	}
	f, err := opentype.NewFace(ft, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err)
	}
	faces[key] = f
	return f
}

// textWidth is how wide s draws, in pixels.
func textWidth(f font.Face, s string) int { return font.MeasureString(f, s).Ceil() }

// fit shortens s with an ellipsis until it fits in w pixels.
func fit(f font.Face, s string, w int) string {
	if textWidth(f, s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 {
		r = r[:len(r)-1]
		if t := strings.TrimRight(string(r), " ") + "…"; textWidth(f, t) <= w {
			return t
		}
	}
	return ""
}

// wrapText splits s into at most n lines of w pixels.
func wrapText(f font.Face, s string, w, n int) []string {
	var lines []string
	words := strings.Fields(s)
	cur := ""
	for _, word := range words {
		try := word
		if cur != "" {
			try = cur + " " + word
		}
		if textWidth(f, try) <= w || cur == "" {
			cur = try
			continue
		}
		lines = append(lines, cur)
		cur = word
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) > n {
		last := strings.Join(lines[n-1:], " ")
		lines = append(lines[:n-1], last)
	}
	for i := range lines {
		lines[i] = fit(f, lines[i], w)
	}
	return lines
}

// text draws s with its left at x and its middle at y, as lit pixels.
func (c *Canvas) text(x, midY int, s string, f font.Face) {
	m := f.Metrics()
	ascent, descent := m.Ascent.Ceil(), m.Descent.Ceil()
	base := midY + (ascent-descent)/2
	w := textWidth(f, s)
	if w <= 0 {
		return
	}
	img := image.NewAlpha(image.Rect(0, 0, w+2, ascent+descent+2))
	d := font.Drawer{Dst: img, Src: image.NewUniform(color.Alpha{255}), Face: f, Dot: fixed.P(0, ascent)}
	d.DrawString(s)
	for py := 0; py < img.Bounds().Dy(); py++ {
		for px := 0; px < img.Bounds().Dx(); px++ {
			if img.AlphaAt(px, py).A >= 128 {
				c.Set(x+px, base-ascent+py, true)
			}
		}
	}
}

// bar draws a progress bar: an outline filled to percent.
func (c *Canvas) bar(x, y, w, h, percent int) {
	percent = min(max(percent, 0), 100)
	for i := 0; i < w; i++ {
		c.Set(x+i, y, true)
		c.Set(x+i, y+h-1, true)
	}
	for j := 0; j < h; j++ {
		c.Set(x, y+j, true)
		c.Set(x+w-1, y+j, true)
	}
	fill := (w - 4) * percent / 100
	for j := 2; j < h-2; j++ {
		for i := 0; i < fill; i++ {
			c.Set(x+2+i, y+j, true)
		}
	}
}

// Line is a line of a GameSense screen: text (bold or not, wrapped onto
// up to Wrap lines) or a progress bar.
type Line struct {
	Text  string
	Bar   bool
	Value int // the bar's percentage
	Bold  bool
	Wrap  int
}

// IconWidth is the room an event icon takes on the left.
const IconWidth = 32

// Render lays out a screen as GameSense does: the icon (rows of
// 32-pixel-wide bits) on the left, the lines shared out top to bottom
// over the rest, text left-aligned and shortened to fit.
func Render(w, h int, icon [][]byte, lines []Line) *Canvas {
	c := New(w, h)
	left := 0
	if len(icon) > 0 {
		top := (h - len(icon)) / 2
		for y, row := range icon {
			for bx, b := range row {
				for bit := 0; bit < 8; bit++ {
					if b&(1<<(7-uint(bit))) != 0 {
						c.Set(bx*8+bit, top+y, true)
					}
				}
			}
		}
		left = IconWidth
	}
	// Wrapped text takes more than one row.
	type row struct {
		text string
		bar  bool
		val  int
		bold bool
	}
	n := 0
	for _, l := range lines {
		if l.Bar || l.Wrap <= 1 {
			n++
		} else {
			n += l.Wrap
		}
	}
	if n == 0 {
		return c
	}
	size := h/n - 2
	if n == 1 {
		size = min(h/2, 18)
	}
	size = min(max(size, 9), 18)
	var rows []row
	room := w - left - 2
	for _, l := range lines {
		switch {
		case l.Bar:
			rows = append(rows, row{bar: true, val: l.Value})
		case l.Wrap > 1:
			wrapped := wrapText(face(size, l.Bold), l.Text, room, l.Wrap)
			for i := 0; i < l.Wrap; i++ {
				t := ""
				if i < len(wrapped) {
					t = wrapped[i]
				}
				rows = append(rows, row{text: t, bold: l.Bold})
			}
		default:
			rows = append(rows, row{text: fit(face(size, l.Bold), l.Text, room), bold: l.Bold})
		}
	}
	rowH := h / len(rows)
	for i, r := range rows {
		top := i * rowH
		if r.bar {
			bh := min(rowH-2, 10)
			c.bar(left+2, top+(rowH-bh)/2, w-left-4, bh, r.val)
			continue
		}
		c.text(left+2, top+rowH/2, r.text, face(size, r.bold))
	}
	return c
}

// Image turns a canvas into an image, for tests and previews.
func (c *Canvas) Image() *image.Gray {
	img := image.NewGray(image.Rect(0, 0, c.W, c.H))
	draw.Draw(img, img.Bounds(), image.Black, image.Point{}, draw.Src)
	for y := 0; y < c.H; y++ {
		for x := 0; x < c.W; x++ {
			if c.At(x, y) {
				img.SetGray(x, y, color.Gray{255})
			}
		}
	}
	return img
}
