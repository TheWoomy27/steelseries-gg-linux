package devicert

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

type nullTransport struct{}

func (nullTransport) Chunk(Target, int, int, []byte, bool, time.Duration, time.Duration) ([]byte, error) {
	return nil, nil
}
func (nullTransport) Connected(Target) bool { return false }

// TestLoadAllSpecs evaluates every imported device specification. It needs
// the data imported by gg-setup; point GG_LINUX_DATA at it.
func TestLoadAllSpecs(t *testing.T) {
	data := os.Getenv("GG_LINUX_DATA")
	if data == "" {
		t.Skip("GG_LINUX_DATA not set")
	}
	specs := filepath.Join(data, "engine", "deviceSpecifications")
	rt, err := New(specs, filepath.Join(data, "engine"), nullTransport{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := rt.KnownIDs()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	failed := 0
	for _, id := range ids {
		if _, err := rt.Device(id); err != nil {
			failed++
			t.Logf("device %#x: %v", id, err)
		}
	}
	counts := map[string]int{}
	for _, e := range rt.LoadErrors {
		i := strings.LastIndex(e, ": ")
		counts[e[i+2:]]++
	}
	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range counts {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	for i, e := range list {
		if i > 40 {
			break
		}
		t.Logf("%4d  %s", e.v, e.k)
	}
	t.Logf("%d devices, %d failed to declare, %d form errors", len(ids), failed, len(rt.LoadErrors))
}

// TestAeroxRewritesOnPair runs the Aerox 5 Wireless spec's on_mouse_paired,
// which rewrites the settings deployed while the mouse slept when it wakes.
func TestAeroxRewritesOnPair(t *testing.T) {
	data := os.Getenv("GG_LINUX_DATA")
	if data == "" {
		t.Skip("GG_LINUX_DATA not set")
	}
	tr := &recordingTransport{asleep: true}
	rt, err := New(filepath.Join(data, "engine", "deviceSpecifications"), filepath.Join(data, "engine"), tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := rt.Device(0x10381852) // the dongle
	if err != nil {
		t.Fatal(err)
	}
	for api, v := range map[string]map[string]any{
		"low_power_gaming_mode_sensor": {"lpgm_mode_on": 1},
		"polling_rate":                 {"level": 2},
	} {
		if err := d.WriteApi(api, v); err == nil {
			t.Fatalf("%s: the write should fail while the mouse sleeps", api)
		}
	}
	tr.asleep, tr.sent = false, nil
	if _, err := d.Call("on_mouse_paired"); err != nil {
		t.Fatalf("on_mouse_paired: %v", err)
	}
	// Tunnelled to the mouse, the commands carry the 0x40 flag.
	var lpgm, polling bool
	for _, c := range tr.sent {
		if len(c.data) >= 3 && c.data[1] == 0x40|0x28 && c.data[2] == 1 {
			lpgm = true
		}
		if len(c.data) >= 3 && c.data[1] == 0x40|0x2b && c.data[2] == 2 {
			polling = true
		}
	}
	if !lpgm || !polling {
		t.Errorf("on waking: low power mode rewritten %v, polling rate %v", lpgm, polling)
	}
}
