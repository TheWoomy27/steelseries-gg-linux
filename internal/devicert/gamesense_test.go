package devicert

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGameSenseInfo reads GameSense zones from the specifications of the
// Apex 7 and the Aerox 5 Wireless. It needs the data imported by
// gg-setup; point GG_LINUX_DATA at it.
func TestGameSenseInfo(t *testing.T) {
	data := os.Getenv("GG_LINUX_DATA")
	if data == "" {
		t.Skip("GG_LINUX_DATA not set")
	}
	rt, err := New(filepath.Join(data, "engine", "deviceSpecifications"), filepath.Join(data, "engine"), nullTransport{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	apex, err := rt.Device(0x10381612)
	if err != nil {
		t.Fatal(err)
	}
	info, err := apex.GameSense()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Apex 7: types %v, %d zones, remap %v", info.Types, len(info.Zones), info.Remap)
	has := func(ts []string, x string) bool {
		for _, t := range ts {
			if t == x {
				return true
			}
		}
		return false
	}
	if !has(info.Types, "rgb-per-key-zones") || !has(info.Types, "keyboard") {
		t.Errorf("Apex 7 types %v", info.Types)
	}
	if fk := info.Zones["function-keys"]; len(fk) != 12 || fk[0] != 0x3A {
		t.Errorf("function keys %v", fk)
	}
	if q := info.Zones["q-row"]; len(q) != 10 || q[0] != 0x14 {
		t.Errorf("q row %v", q)
	}
	if info.Remap[0x65] != 0xF0 {
		t.Errorf("remap %v", info.Remap)
	}
	if info.ScreenSizes["one"] != [2]int{128, 40} || info.ScreenLines["one"] != 2 || info.ScreenZones["one"] != 0 {
		t.Errorf("screen %v %v %v", info.ScreenZones, info.ScreenSizes, info.ScreenLines)
	}
	aerox, err := rt.Device(0x10381852)
	if err != nil {
		t.Fatal(err)
	}
	info, err = aerox.GameSense()
	if err != nil {
		t.Fatal(err)
	}
	if !has(info.Types, "rgb-3-zone") || len(info.Zones["two"]) != 1 || info.Zones["two"][0] != 1 || len(info.Zones["all"]) != 3 {
		t.Errorf("Aerox 5 Wireless: %+v", info)
	}
}
