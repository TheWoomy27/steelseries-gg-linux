package engine

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ggl/internal/devicert"
)

// gatedTransport counts the reports written; the first one waits for
// release, as a slow device write does.
type gatedTransport struct {
	mu      sync.Mutex
	writes  int
	first   bool
	entered chan struct{}
	release chan struct{}
}

func (g *gatedTransport) Chunk(devicert.Target, int, int, []byte, bool, time.Duration, time.Duration) ([]byte, error) {
	g.mu.Lock()
	g.writes++
	first := !g.first
	g.first = true
	g.mu.Unlock()
	if first {
		close(g.entered)
		<-g.release
	}
	return nil, nil
}

func (g *gatedTransport) Connected(devicert.Target) bool { return true }

func (g *gatedTransport) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.writes
}

const sequencerSpec = `
(device 0x10380001
    (usage-page 0xffc0)
    (usage 0x0001)
    (struct color
        (unaligned)
        (field report_id uint8 (constant 0x00))
        (field command uint8 (constant 0x05))
        (field red uint8)
        (field green uint8)
        (field blue uint8))
    (api color
        (api-write HID (chunk HIDIO 65 payload))))
`

// Two plays of the same sequence at once (two deploys overlapping) leave
// one animation running, which stopping the device ends.
func TestSequencerOverlappingPlaysLeaveNoAnimation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.lsp"), []byte(sequencerSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &gatedTransport{entered: make(chan struct{}), release: make(chan struct{})}
	rt, err := devicert.New(dir, dir, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := rt.Device(0x10380001)
	if err != nil {
		t.Fatal(err)
	}
	s := newSequencer()
	frames := func(a, b float64) any {
		return map[string]any{"data": []any{[]any{a, 0.0, 0.0}, []any{b, 0.0, 0.0}}}
	}
	done := make(chan error)
	go func() { done <- s.Play(d, "color", nil, frames(1, 2)) }()
	<-tr.entered // the first play is writing its first frame
	// The second play waits for the device (one command at a time), so it
	// runs alongside; the first frame is let through once it's queued.
	done2 := make(chan error)
	go func() { done2 <- s.Play(d, "color", nil, frames(3, 4)) }()
	time.Sleep(50 * time.Millisecond)
	close(tr.release)
	if err := <-done2; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	s.StopDevice(d.ID)
	time.Sleep(100 * time.Millisecond) // a frame in flight may still land
	n := tr.count()
	time.Sleep(300 * time.Millisecond)
	if more := tr.count() - n; more > 0 {
		t.Errorf("%d frames written after the device's animations were stopped", more)
	}
}
