package hid

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// fakeHidraw lays out /sys/class/hidraw/<node> and /dev/<node> for a
// device: its uevent under the HID device directory instance (as the
// kernel names it, 0003:1038:PPPP.NNNN), and a FIFO standing for the
// node.
type fakeHidraw struct{ sys, dev, devices string }

func newFakeHidraw(t *testing.T) *fakeHidraw {
	root := t.TempDir()
	f := &fakeHidraw{filepath.Join(root, "class"), filepath.Join(root, "dev"), filepath.Join(root, "devices")}
	for _, d := range []string{f.sys, f.dev, f.devices} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldSys, oldDev := sysClassHidraw, devDir
	sysClassHidraw, devDir = f.sys, f.dev
	t.Cleanup(func() { sysClassHidraw, devDir = oldSys, oldDev })
	return f
}

// plug makes node stand for HID device instance (a product of vendor
// 1038).
func (f *fakeHidraw) plug(t *testing.T, node, instance string, pid uint16) {
	dir := filepath.Join(f.devices, instance)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	uevent := "HID_ID=0003:00001038:0000" + hex4(pid) + "\nHID_NAME=SteelSeries test\n"
	if err := os.WriteFile(filepath.Join(dir, "uevent"), []byte(uevent), 0o644); err != nil {
		t.Fatal(err)
	}
	nodeDir := filepath.Join(f.sys, node)
	os.RemoveAll(nodeDir)
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(nodeDir, "device")); err != nil {
		t.Fatal(err)
	}
	devNode := filepath.Join(f.dev, node)
	if _, err := os.Stat(devNode); err != nil {
		if err := syscall.Mkfifo(devNode, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func hex4(v uint16) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{digits[v>>12&15], digits[v>>8&15], digits[v>>4&15], digits[v&15]})
}

type hotplugLog struct{ added, removed []Info }

func (h *hotplugLog) record(added, removed []Info) {
	h.added = append(h.added, added...)
	h.removed = append(h.removed, removed...)
}

// A device unplugged and plugged back in between two scans (a USB reset,
// a resume from suspend) usually gets the hidraw node it had. The node
// ggd has open is dead then: the manager must drop it and open the new
// one, telling the engine the device went and came back.
func TestScanReopensReplugOnSameNode(t *testing.T) {
	f := newFakeHidraw(t)
	f.plug(t, "hidraw3", "0003:1038:1640.0004", 0x1640)
	var log hotplugLog
	m := NewManager(log.record)
	defer m.Close()
	m.Scan()
	if len(log.added) != 1 || len(log.removed) != 0 {
		t.Fatalf("first scan: added %d removed %d", len(log.added), len(log.removed))
	}
	old := m.Devices(0x10381640)[0]

	f.plug(t, "hidraw3", "0003:1038:1640.0009", 0x1640)
	log = hotplugLog{}
	m.Scan()
	if len(log.removed) != 1 || len(log.added) != 1 {
		t.Fatalf("replugged: added %d removed %d, want 1 and 1", len(log.added), len(log.removed))
	}
	devs := m.Devices(0x10381640)
	if len(devs) != 1 || devs[0] == old {
		t.Fatalf("the dead node is still in use")
	}
	select {
	case <-old.closed:
	default:
		t.Errorf("the dead node was left open")
	}
}

// A node handed to another product is the old device's unplug: the old
// node is closed and reported removed, not leaked.
func TestScanClosesNodeTakenByAnotherProduct(t *testing.T) {
	f := newFakeHidraw(t)
	f.plug(t, "hidraw3", "0003:1038:1640.0004", 0x1640)
	var log hotplugLog
	m := NewManager(log.record)
	defer m.Close()
	m.Scan()
	old := m.Devices(0x10381640)[0]

	f.plug(t, "hidraw3", "0003:1038:170E.0005", 0x170e)
	log = hotplugLog{}
	m.Scan()
	if len(log.removed) != 1 || log.removed[0].ProductID != 0x1640 {
		t.Errorf("removed %+v, want the 1640", log.removed)
	}
	if len(log.added) != 1 || log.added[0].ProductID != 0x170e {
		t.Errorf("added %+v, want the 170e", log.added)
	}
	select {
	case <-old.closed:
	default:
		t.Errorf("the old node was left open")
	}
	if len(m.Devices(0x10381640)) != 0 || len(m.Devices(0x1038170e)) != 1 {
		t.Errorf("devices after the swap: 1640 %d, 170e %d", len(m.Devices(0x10381640)), len(m.Devices(0x1038170e)))
	}
}

// An unchanged node stays open across scans.
func TestScanKeepsUnchangedNode(t *testing.T) {
	f := newFakeHidraw(t)
	f.plug(t, "hidraw3", "0003:1038:1640.0004", 0x1640)
	var log hotplugLog
	m := NewManager(log.record)
	defer m.Close()
	m.Scan()
	old := m.Devices(0x10381640)[0]
	log = hotplugLog{}
	m.Scan()
	if len(log.added)+len(log.removed) != 0 || m.Devices(0x10381640)[0] != old {
		t.Errorf("an unchanged node was reopened")
	}
}
