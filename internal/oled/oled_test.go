package oled

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestPack(t *testing.T) {
	c := New(128, 40)
	c.Set(0, 0, true)    // first byte, high bit
	c.Set(9, 0, true)    // second byte, bit 6
	c.Set(127, 39, true) // last byte, low bit
	b := c.Pack()
	if len(b) != 640 || b[0] != 0x80 || b[1] != 0x40 || b[639] != 0x01 {
		t.Fatalf("packed % x ... % x", b[:2], b[639])
	}
	if u := Unpack(128, 40, b); !u.At(9, 0) || !u.At(127, 39) || u.At(1, 0) {
		t.Error("unpack")
	}
	v := c.PackVertical()
	if v[0] != 0x01 || v[9] != 0x01 || v[127+4*128] != 0x80 {
		t.Errorf("vertical % x", v[:10])
	}
}

func lit(c *Canvas, x0, y0, x1, y1 int) int {
	n := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if c.At(x, y) {
				n++
			}
		}
	}
	return n
}

func TestRender(t *testing.T) {
	// Two lines: text in the top half, a half-full bar in the bottom.
	c := Render(128, 40, nil, []Line{{Text: "Health", Bold: true}, {Bar: true, Value: 50}})
	if lit(c, 0, 0, 128, 20) < 20 {
		t.Error("no text on the top line")
	}
	full := lit(c, 4, 27, 64, 33) // inside the outline
	empty := lit(c, 66, 27, 124, 33)
	if full < 100 || empty > 4 {
		t.Errorf("bar: %d lit in the first half, %d in the second", full, empty)
	}
	// Text too long for the screen is shortened, not drawn past it.
	c = Render(128, 40, nil, []Line{{Text: "A very long song title that cannot possibly fit"}})
	if lit(c, 126, 0, 128, 40) != 0 {
		t.Error("text ran off the edge")
	}
	// The icon takes the left 32 pixels.
	icon := [][]byte{{0xff, 0xff, 0xff, 0xff}, {0xff, 0xff, 0xff, 0xff}}
	c = Render(128, 40, icon, []Line{{Text: "x"}})
	if !c.At(0, 19) || !c.At(31, 20) || c.At(32, 19) {
		t.Error("icon")
	}
	if dir := os.Getenv("GG_LINUX_OLED_PNG"); dir != "" {
		for name, c := range map[string]*Canvas{
			"two-lines": Render(128, 40, nil, []Line{{Text: "Never Gonna Give You Up", Bold: true}, {Text: "Rick Astley"}}),
			"bar":       Render(128, 40, nil, []Line{{Text: "Health 72", Bold: true}, {Bar: true, Value: 72}}),
			"one-line":  Render(128, 40, nil, []Line{{Text: "12:45", Bold: true}}),
			"wrap":      Render(128, 40, nil, []Line{{Text: "A message long enough to wrap onto two lines", Wrap: 2}}),
		} {
			f, _ := os.Create(filepath.Join(dir, name+".png"))
			png.Encode(f, c.Image())
			f.Close()
		}
	}
}

func TestLoadIcons(t *testing.T) {
	path := filepath.Join(os.Getenv("GG_LINUX_DATA"), "engine", "game-integration", "lib", "screener.lsp")
	if os.Getenv("GG_LINUX_DATA") == "" {
		t.Skip("GG_LINUX_DATA not set")
	}
	icons, err := LoadIcons(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(icons) < 30 || len(icons[1]) != 20 || len(icons[1][0]) != 4 {
		t.Fatalf("%d icons; icon 1: %d rows", len(icons), len(icons[1]))
	}
}
