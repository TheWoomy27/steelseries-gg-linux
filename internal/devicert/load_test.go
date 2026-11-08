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
