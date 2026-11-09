package devicert

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPrismData reads the PrismSync layout of real specs (from the data
// gg-setup imports): every zone's colour must land inside its report, in
// the report's colour fields.
func TestPrismData(t *testing.T) {
	data := os.Getenv("GG_LINUX_DATA")
	if data == "" {
		t.Skip("GG_LINUX_DATA not set")
	}
	rt, err := New(filepath.Join(data, "engine", "deviceSpecifications"), filepath.Join(data, "engine"), nullTransport{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		id    uint32
		zones int
		typ   int
	}{
		{"Apex 7", 0x10381612, 104, HIDFeature},
		{"Alias", 0x10381B04, 1, HIDIO},
		{"Aerox 3 Wireless", 0x10381838, 3, HIDIO},
	} {
		if only := os.Getenv("PRISM_ONLY"); only != "" && only != c.name {
			continue
		}
		t.Logf("%s: loading", c.name)
		d, err := rt.Device(c.id)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		t.Logf("%s: reading prism data", c.name)
		p, err := d.PrismData()
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !p.Supported || !p.Whitelisted || len(p.ZoneWrites) == 0 {
			t.Errorf("%s: %+v", c.name, p)
			continue
		}
		t.Logf("%s: %d zones, %d writes (type %d, %d bytes, read %v), init %d, step %d ms, plane %d, bitmap %v hid %v",
			c.name, len(p.Zones), len(p.ZoneWrites), p.ZoneWrites[0].Type, p.ZoneWrites[0].Size, p.ZoneWrites[0].Read, len(p.InitWrites), p.StepMs, p.CoordinatePlane, p.HasBitmap, p.HasHidCodes)
		if len(p.Zones) < c.zones || p.ZoneWrites[0].Type != c.typ {
			t.Errorf("%s: %d zones (want %d), write type %d", c.name, len(p.Zones), c.zones, p.ZoneWrites[0].Type)
		}
		for _, z := range p.Zones {
			w := p.ZoneWrites[z.Write]
			if z.Red >= len(w.Data) || z.Green != z.Red+1 || z.Blue != z.Red+2 {
				t.Errorf("%s: zone %+v outside its report", c.name, z)
				break
			}
		}
	}
}
