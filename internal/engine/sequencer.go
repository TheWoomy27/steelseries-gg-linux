package engine

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"ggl/internal/devicert"
)

// sequencer plays lighting colour sequences. A sequence setting looks like
// {"data": [[r,g,b], ...], "repeat": N}: precomputed frames that are written
// through the sequence's template api every SequenceStepMs. Static colours
// are a single frame and are written once.
type sequencer struct {
	mu      sync.Mutex
	running map[string]chan struct{}
}

func newSequencer() *sequencer { return &sequencer{running: map[string]chan struct{}{}} }

func frameValues(frame any) map[string]any {
	switch f := frame.(type) {
	case []any:
		if len(f) >= 3 {
			return map[string]any{"red": f[0], "green": f[1], "blue": f[2]}
		}
		if len(f) == 1 {
			return map[string]any{"level": f[0]}
		}
	case map[string]any:
		return f
	case float64, int64, int:
		return map[string]any{"level": f}
	}
	return nil
}

func (s *sequencer) stop(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.running[key]; ok {
		close(ch)
		delete(s.running, key)
	}
}

// StopDevice ends every animation for a device (on disconnect/shutdown).
func (s *sequencer) StopDevice(id uint32) {
	prefix := fmt.Sprintf("%x/", id)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, ch := range s.running {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			close(ch)
			delete(s.running, k)
		}
	}
}

func (s *sequencer) Play(d *devicert.Decl, api string, seq *devicert.Sequence, value any) error {
	key := fmt.Sprintf("%x/%s", d.ID, api)
	s.stop(key)
	m, _ := value.(map[string]any)
	frames, _ := m["data"].([]any)
	if len(frames) == 0 {
		return nil
	}
	if allSame(frames) {
		return d.WriteApi(api, frameValues(frames[0]))
	}
	if err := d.WriteApi(api, frameValues(frames[0])); err != nil {
		return err
	}
	step := time.Duration(d.SequenceStepMs) * time.Millisecond
	if step <= 0 {
		step = 33 * time.Millisecond
	}
	stop := make(chan struct{})
	s.mu.Lock()
	s.running[key] = stop
	s.mu.Unlock()
	go func() {
		t := time.NewTicker(step)
		defer t.Stop()
		i := 1
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			if err := d.WriteApi(api, frameValues(frames[i%len(frames)])); err != nil {
				slog.Debug("sequence frame", "device", d.Name, "api", api, "err", err)
				if !d.Connected() {
					s.stop(key)
					return
				}
			}
			i++
		}
	}()
	return nil
}

func allSame(frames []any) bool {
	first := fmt.Sprint(frames[0])
	for _, f := range frames[1:] {
		if fmt.Sprint(f) != first {
			return false
		}
	}
	return true
}
