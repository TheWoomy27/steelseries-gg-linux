package engine

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"ggl/internal/devicert"
)

// lit counts the lit pixels in a part of a picture.
func lit(pic []byte, x0, y0, x1, y1 int) int {
	n := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if i := y*128 + x; pic[i>>3]&(0x80>>uint(i&7)) != 0 {
				n++
			}
		}
	}
	return n
}

func TestProDisplayFrame(t *testing.T) {
	c := proDisplayFrame(map[string]int{"MouseBatt": 85, "BaseBatt": 100, "DpiX": 1600, "DpiY": 800, "Poll": 8000})
	if dir := os.Getenv("GG_LINUX_PICTURES"); dir != "" {
		f, _ := os.Create(filepath.Join(dir, "pro-display.png"))
		png.Encode(f, c.Image())
		f.Close()
	}
	pic := c.Pack()
	if len(pic) != 640 {
		t.Fatalf("%d bytes, want 640 (128x40)", len(pic))
	}
	for _, q := range [][4]int{{0, 0, 64, 20}, {64, 0, 128, 20}, {0, 20, 64, 40}, {64, 20, 128, 40}} {
		if lit(pic, q[0], q[1], q[2], q[3]) == 0 {
			t.Errorf("nothing drawn in %v", q)
		}
	}
	// A dongle reports no base battery, and nothing is drawn for it.
	pic = proDisplayFrame(map[string]int{"MouseBatt": 50, "DpiX": 800, "DpiY": 800}).Pack()
	if n := lit(pic, 112, 0, 128, 40); n != 0 {
		t.Errorf("%d pixels lit at the right with no base battery or rate", n)
	}
	if got := shortHz(8000) + " " + shortHz(500) + " " + shortHz(1500); got != "8k 500 1.5k" {
		t.Errorf("rates %q", got)
	}
}

// As the 2024 Apex Pro keyboards' spec shows the display: packed by
// columns, written with their GameSense screen command.
const proScreenSpec = `
(device 0x10380006
    (usage-page 0xffc0)
    (usage 0x0001)
    (struct oled_direct_write
        (unaligned)
        (field report_id uint8 (constant 0x00))
        (field command uint8 (constant 0x0A))
        (field data bytearray (size 640)))
    (api oled_direct_write
        (api-write HID (chunk HIDIO 643 payload)))
    (define (write-shihan-monitoring-data pixels)
        (write-to-device 0x10380006 (lisp->json {oled_direct_write: {data: (bytearray->list (convert-to-column-packed-byte-format (list->bytearray pixels) 128 40))}}) #t)))
`

func TestProDisplayWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.lsp"), []byte(proScreenSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &sentTransport{}
	rt, err := devicert.New(dir, dir, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := rt.Device(0x10380006)
	if err != nil {
		t.Fatal(err)
	}
	pic := make([]byte, 640)
	pic[0] = 0x80 // the top left pixel
	if err := d.MonitoringWrite(pic); err != nil {
		t.Fatal(err)
	}
	if len(tr.sent) != 1 || len(tr.sent[0]) < 3 || tr.sent[0][1] != 0x0a || tr.sent[0][2] != 0x01 {
		t.Fatalf("wrote %x", tr.sent)
	}
}
