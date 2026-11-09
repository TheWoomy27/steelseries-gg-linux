package hid

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The boot keyboard report descriptor from the HID specification.
var bootKeyboard = []byte{
	0x05, 0x01, 0x09, 0x06, 0xa1, 0x01, 0x05, 0x07, 0x19, 0xe0, 0x29, 0xe7, 0x15, 0x00, 0x25, 0x01,
	0x75, 0x01, 0x95, 0x08, 0x81, 0x02, 0x95, 0x01, 0x75, 0x08, 0x81, 0x01, 0x95, 0x05, 0x75, 0x01,
	0x05, 0x08, 0x19, 0x01, 0x29, 0x05, 0x91, 0x02, 0x95, 0x01, 0x75, 0x03, 0x91, 0x01, 0x95, 0x06,
	0x75, 0x08, 0x15, 0x00, 0x25, 0x65, 0x05, 0x07, 0x19, 0x00, 0x29, 0x65, 0x81, 0x00, 0xc0,
}

func TestPressedKeys(t *testing.T) {
	d := ParseDescriptor(bootKeyboard)
	if len(d.Keys) != 2 {
		t.Fatalf("key fields: %+v", d.Keys)
	}
	// Left shift held, A (4) and S (22) pressed.
	got := d.PressedKeys([]byte{0, 0x02, 0, 4, 22, 0, 0, 0, 0})
	slices.Sort(got)
	if !slices.Equal(got, []uint8{4, 22, 225}) {
		t.Errorf("pressed %v", got)
	}
	if got := d.PressedKeys([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0}); len(got) != 0 {
		t.Errorf("nothing pressed, got %v", got)
	}
}

// TestRealKeyboards parses the descriptors of SteelSeries keyboards
// plugged in, if any.
func TestRealKeyboards(t *testing.T) {
	nodes, _ := filepath.Glob("/sys/class/hidraw/hidraw*/device/report_descriptor")
	for _, n := range nodes {
		b, err := os.ReadFile(n)
		if err != nil {
			continue
		}
		uevent, _ := os.ReadFile(filepath.Join(filepath.Dir(n), "uevent"))
		d := ParseDescriptor(b)
		if len(d.Keys) > 0 {
			t.Logf("%s: %d key fields %+v", filepath.Base(filepath.Dir(filepath.Dir(n))), len(d.Keys), d.Keys)
			_ = uevent
		}
	}
}
