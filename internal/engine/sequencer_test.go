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

// recordingTransport keeps every report written.
type recordingTransport struct {
	mu     sync.Mutex
	writes [][]byte
}

func (r *recordingTransport) Chunk(_ devicert.Target, _ int, _ int, data []byte, _ bool, _ time.Duration, _ time.Duration) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, append([]byte(nil), data...))
	return nil, nil
}

func (r *recordingTransport) Connected(devicert.Target) bool { return true }

func (r *recordingTransport) take() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.writes
	r.writes = nil
	return w
}

// The legacy Apex keyboards' spec shape: a struct with the sequence api's
// name stores a picture with 0x65 (which their firmware ignores), and the
// sequence's template draws it with 0x61.
const screenSequenceSpec = `
(device 0x10380002
    (usage-page 0xffc0)
    (usage 0x0001)
    (struct oled_display_data
        (field data bytearray (size 8)))
    (struct oled_display
        (unaligned)
        (field report_id uint8 (constant 0x00))
        (field command uint8 (constant 0x65))
        (fields-from-struct oled_display_data))
    (api oled_display
        (api-write HID (chunk HIDFEATURE 10 payload)))
    (template macho_oled_display
        (unaligned)
        (field report_id uint8 (constant 0x00))
        (field command uint8 (constant 0x61))
        (fields-from-struct oled_display_data))
    (sequence oled_display
        macho_oled_display
        macho_oled_display
        (api-write HID (chunk HIDFEATURE 10 payload))))
`

// A screen sequence's frames are pictures: they go through the template
// (0x61) with their bytes, they wait while a GameSense screen has the
// device, and the picture is drawn again when the screen is given back.
func TestSequencerScreenFrames(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.lsp"), []byte(screenSequenceSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &recordingTransport{}
	rt, err := devicert.New(dir, dir, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := rt.Device(0x10380002)
	if err != nil {
		t.Fatal(err)
	}
	api, seq := d.SequenceForSetting("macho_oled_display_sequence")
	if api != "oled_display" || seq == nil {
		t.Fatalf("SequenceForSetting = %q, %v", api, seq)
	}
	picture := func(b float64) any {
		f := make([]any, 8)
		for i := range f {
			f[i] = b
		}
		return map[string]any{"data": []any{f}}
	}
	check := func(what string, b byte) {
		t.Helper()
		w := tr.take()
		if len(w) != 1 {
			t.Fatalf("%s: %d reports written, want 1", what, len(w))
		}
		want := []byte{0x00, 0x61, b, b, b, b, b, b, b, b}
		if string(w[0][:len(want)]) != string(want) {
			t.Fatalf("%s: wrote % x, want % x", what, w[0], want)
		}
	}
	s := newSequencer()
	if err := s.Play(d, api, seq, picture(0xaa)); err != nil {
		t.Fatal(err)
	}
	check("play", 0xaa)

	s.PauseScreen(d.ID)
	if err := s.Play(d, api, seq, picture(0x55)); err != nil {
		t.Fatal(err)
	}
	if w := tr.take(); len(w) != 0 {
		t.Fatalf("paused screen: %d reports written, want none", len(w))
	}
	if !s.ResumeScreen(d.ID) {
		t.Fatal("ResumeScreen reported no picture")
	}
	check("resume", 0x55)

	s.StopDevice(d.ID)
	if s.ResumeScreen(d.ID) {
		t.Fatal("ResumeScreen drew a picture after the device stopped")
	}
}
