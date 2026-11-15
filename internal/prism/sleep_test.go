package prism

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ggl/internal/devicert"
	"ggl/internal/paths"
)

type fakeSleep struct{ on bool }

func (f *fakeSleep) asleep() bool { return f.on }

// useSleepWatcher makes monitorsAsleep ask w for the rest of the test.
func useSleepWatcher(t *testing.T, w sleepWatcher) {
	old := newSleepWatcher
	newSleepWatcher = func() sleepWatcher { return w }
	sleepOnce = sync.Once{}
	t.Cleanup(func() { newSleepWatcher, sleepOnce, sleepSrc = old, sync.Once{}, nil })
}

func TestMonitorsAsleepAsksTheWatcher(t *testing.T) {
	made := 0
	f := &fakeSleep{}
	useSleepWatcher(t, f)
	newSleepWatcher = func() sleepWatcher { made++; return f }
	if monitorsAsleep() {
		t.Fatal("asleep while the watcher says awake")
	}
	f.on = true
	if !monitorsAsleep() {
		t.Fatal("awake while the watcher says asleep")
	}
	if made != 1 {
		t.Errorf("watcher made %d times, want once", made)
	}
}

func TestPickSleepWatcher(t *testing.T) {
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "x")
	if _, ok := newSleepWatcher().(*hyprlandSleep); !ok {
		t.Error("Hyprland: not asking hyprctl")
	}
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if _, ok := newSleepWatcher().(noSleep); !ok {
		t.Error("no Wayland: should count the monitors awake")
	}
}

func TestDimWhileAsleep(t *testing.T) {
	fe := &fakeEngine{devs: testDevices(), frames: map[int][]devicert.PrismWrite{}}
	devs := []*device{newDevice(fe.devs[0], nil, false)}
	c := parseConfig(colorShift)
	layer := ConfigData{Base: c.Base}
	awake := renderZonesAsleep(&layer, devs, 0, false, nil, nil)

	// Without a dim factor, sleeping monitors change nothing.
	if usesDimming(&layer.Base) {
		t.Fatal("no dim factor, yet dimming")
	}
	same := renderZonesAsleep(&layer, devs, 0, true, nil, nil)
	for id, col := range awake {
		if same[id] != col {
			t.Errorf("zone %s: %v asleep, %v awake", id, same[id], col)
		}
	}

	layer.Base.GlobalConfig.DimFactorOnMonitorSleep = 255 // "Turn off"
	if !usesDimming(&layer.Base) {
		t.Fatal("dim factor set, yet not dimming")
	}
	lit := false
	for id, col := range renderZonesAsleep(&layer, devs, 0, true, nil, nil) {
		if col != (Color{}) {
			t.Errorf("zone %s lit %v while the monitors sleep", id, col)
		}
		if awake[id] != (Color{}) {
			lit = true
		}
	}
	if !lit {
		t.Fatal("nothing lit while awake; the test proves nothing")
	}
}

// fakeDisplay answers a Wayland client's round trips and offers no
// globals: a compositor without org_kde_kwin_dpms. Its first connection
// drops at once (the compositor restarting).
func fakeDisplay(t *testing.T) (string, *atomic.Int32) {
	dir := t.TempDir()
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "wayland-9"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	accepted := new(atomic.Int32)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if accepted.Add(1) == 1 {
				c.Close()
				continue
			}
			go func() {
				defer c.Close()
				hdr := make([]byte, 8)
				for {
					if _, err := io.ReadFull(c, hdr); err != nil {
						return
					}
					obj, word := binary.LittleEndian.Uint32(hdr), binary.LittleEndian.Uint32(hdr[4:])
					body := make([]byte, int(word>>16)-8)
					if _, err := io.ReadFull(c, body); err != nil {
						return
					}
					if obj == 1 && uint16(word) == 0 { // sync: done
						msg := binary.LittleEndian.AppendUint32(nil, binary.LittleEndian.Uint32(body))
						msg = binary.LittleEndian.AppendUint32(msg, 12<<16)
						msg = binary.LittleEndian.AppendUint32(msg, 0)
						c.Write(msg)
					}
				}
			}()
		}
	}()
	return dir, accepted
}

func TestKWinSleepReconnectsThenGivesUpWithoutDPMS(t *testing.T) {
	dir, accepted := fakeDisplay(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-9")
	old := kwinRetry
	kwinRetry = 10 * time.Millisecond
	t.Cleanup(func() { kwinRetry = old })

	k := &kwinSleep{}
	done := make(chan struct{})
	go func() { k.run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("kwinSleep kept trying a compositor without org_kde_kwin_dpms")
	}
	if n := accepted.Load(); n != 2 {
		t.Errorf("connected %d times, want 2 (once more after the drop)", n)
	}
	if k.asleep() {
		t.Error("asleep without being told")
	}
}

// TestDimmingKeepsEachZonesEffect is "When Monitor Sleeps" changed with
// zones selected that show different effects: the client then sends the
// dimming alone (see client/linux/patches.js), which every selected zone
// takes while keeping its own effect.
func TestDimmingKeepsEachZonesEffect(t *testing.T) {
	fe := &fakeEngine{devs: testDevices(), frames: map[int][]devicert.PrismWrite{}}
	mig := ""
	if _, err := os.Stat(paths.PrismDBMig()); err == nil {
		mig = paths.PrismDBMig()
	}
	s, err := New(t.TempDir()+"/prism.db", mig, fe, func(string, any) {})
	if err != nil {
		t.Fatal(err)
	}
	s.refreshDevices()
	devs := s.connected()
	c := &configRow{ID: "c1", Name: "Test", Data: parseConfig(colorShift)}
	s.db.Exec(`INSERT INTO configs (id, name, config_json, schema, is_current) VALUES ('c1', 'Test', '{}', 1, 1)`)
	kb0, kb1, mic := devs[0].Zones[0].UniqueID, devs[0].Zones[1].UniqueID, devs[1].Zones[0].UniqueID
	green := Color{0, 255, 0}
	put := func(body string) {
		t.Helper()
		ups, err := parseUpdates([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		s.applyUpdates(c, modeBase, ups, parseSelection([]byte(body)))
	}
	sel := func(ids ...string) string {
		b, _ := json.Marshal(ids)
		return `"selectionInfo":{"deviceSelection":{"mode":"crossDevice"},"zoneSelection":{"zoneIds":` + string(b) + `,"mode":"ids"}}`
	}
	// One key green, the rest the colour shift: two effects.
	put(`{"configurationUpdateData":{"defaultColor":{"red":0,"green":255,"blue":0},"metadata":{"effectType":"SINGLE_COLOR"},"dimFactorOnMonitorSleep":0},` + sel(kb0) + `}`)
	shift := renderZones(&c.Data, devs, 1000)[kb1]
	if shift == green {
		t.Fatal("setup: the colour spread")
	}
	dims := func() map[string]int {
		out := map[string]int{}
		for _, d := range devs {
			for i := range d.Zones {
				out[d.Zones[i].UniqueID] = int(c.Data.Base.resolve(d, &d.Zones[i]).DimFactorOnMonitorSleep)
			}
		}
		return out
	}
	check := func(what string, want map[string]int) {
		t.Helper()
		got := renderZones(&c.Data, devs, 1000)
		if got[kb0] != green || got[kb1] != shift {
			t.Errorf("%s: effects changed: key 0 %v (want %v), key 1 %v (want %v)", what, got[kb0], green, got[kb1], shift)
		}
		if d := dims(); fmt.Sprint(d) != fmt.Sprint(want) {
			t.Errorf("%s: dim factors %v, want %v", what, d, want)
		}
	}

	// "Lighting Disabled" with every zone selected.
	put(`{"configurationUpdateData":{"dimFactorOnMonitorSleep":255},` + sel(kb0, kb1, mic) + `}`)
	check("every zone", map[string]int{kb0: 255, kb1: 255, mic: 255})
	if !usesDimming(&c.Data.Base) {
		t.Error("the layer does not dim")
	}
	for id, col := range renderZonesAsleep(&ConfigData{Base: c.Data.Base}, devs, 1000, true, nil, nil) {
		if col != (Color{}) {
			t.Errorf("zone %s lit %v while the monitors sleep", id, col)
		}
	}

	// "50% Brightness" on both keys (two effects), the mic left alone.
	put(`{"configurationUpdateData":{"dimFactorOnMonitorSleep":160},` + sel(kb0, kb1) + `}`)
	check("two keys", map[string]int{kb0: 160, kb1: 160, mic: 255})

	// A new effect on every zone still replaces the overrides.
	put(`{"configurationUpdateData":{"defaultColor":{"red":9,"green":9,"blue":9},"metadata":{"effectType":"SINGLE_COLOR"},"dimFactorOnMonitorSleep":0},` + sel(kb0, kb1, mic) + `}`)
	if n := len(c.Data.Base.DeviceConfigs); n != 0 || renderZones(&c.Data, devs, 0)[kb0] != (Color{9, 9, 9}) {
		t.Errorf("new effect everywhere: %d overrides left", n)
	}
}
