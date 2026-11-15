package hid

import (
	"os"
	"path/filepath"
	"testing"
)

// plugID makes node stand for a HID device with the given HID_ID
// (bus:vendor:product, as in uevent).
func (f *fakeHidraw) plugID(t *testing.T, node, instance, hidID string) {
	dir := filepath.Join(f.devices, instance)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uevent"), []byte("HID_ID="+hidID+"\nHID_NAME=test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.sys, node), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(f.sys, node, "device")); err != nil {
		t.Fatal(err)
	}
}

// SteelSeries devices over Bluetooth use vendor 0111, and can share a
// product number with a USB device (the Aerox 5 Wireless: 1038:1854 by
// cable, 0111:1854 over Bluetooth); they are told apart by vendor.
func TestEnumerateBluetooth(t *testing.T) {
	f := newFakeHidraw(t)
	f.plugID(t, "hidraw0", "0003:1038:1854.0001", "0003:00001038:00001854")
	f.plugID(t, "hidraw1", "0005:0111:1854.0002", "0005:00000111:00001854")
	f.plugID(t, "hidraw2", "0003:0111:1854.0003", "0003:00000111:00001854") // another vendor's USB device
	f.plugID(t, "hidraw3", "0005:046D:B023.0004", "0005:0000046D:0000B023")
	infos, err := Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint32]string{}
	for _, in := range infos {
		got[in.ID()] = filepath.Base(in.Path)
	}
	want := map[uint32]string{0x10381854: "hidraw0", 0x01111854: "hidraw1"}
	if len(got) != len(want) || got[0x10381854] != "hidraw0" || got[0x01111854] != "hidraw1" {
		t.Errorf("enumerated %v, want %v", got, want)
	}

	// GG_LINUX_ONLY_DEVICES narrows it to the listed ones.
	onlyDevices = parseOnly("0111:1854")
	t.Cleanup(func() { onlyDevices = nil })
	infos, _ = Enumerate()
	if len(infos) != 1 || infos[0].ID() != 0x01111854 {
		t.Errorf("only 0111:1854: %v", infos)
	}
}
