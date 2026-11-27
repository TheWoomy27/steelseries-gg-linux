package engine

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"ggl/internal/devicert"
)

// sequencer plays sequences. A lighting sequence setting looks like
// {"data": [[r,g,b], ...], "repeat": N}: precomputed frames that are written
// through the sequence's api every SequenceStepMs. Static colours are a
// single frame and are written once.
//
// A screen sequence (an OLED picture or GIF, such as the legacy Apex
// keyboards' macho_oled_display_sequence) has frames of picture bytes. Those
// go through the sequence's template, which draws on the screen for as long
// as nothing else does: while a GameSense screen shows, the sequencer holds
// them back (as the specs' internal-screen-initialize stops the sequence on
// Windows), and it draws the picture again when the screen is given back.
type sequencer struct {
	mu      sync.Mutex
	running map[string]chan struct{}
	screens map[string]*screenSeq // the screen sequence each key plays
	paused  map[uint32]bool       // devices whose screen GameSense has
}

// screenSeq is a screen sequence's device and the frame it shows now.
type screenSeq struct {
	d     *devicert.Decl
	seq   *devicert.Sequence
	frame map[string]any
}

func newSequencer() *sequencer {
	return &sequencer{running: map[string]chan struct{}{}, screens: map[string]*screenSeq{}, paused: map[uint32]bool{}}
}

func frameValues(frame any) map[string]any {
	switch f := frame.(type) {
	case []any:
		if isScreenFrame(f) {
			return map[string]any{"data": f}
		}
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

// isScreenFrame reports whether a frame is a picture (bytes for a screen)
// rather than a colour or a level.
func isScreenFrame(frame any) bool {
	f, ok := frame.([]any)
	return ok && len(f) > 4
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
	for k := range s.screens {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			delete(s.screens, k)
		}
	}
	delete(s.paused, id)
}

// PauseScreen holds a device's screen sequences back while a GameSense
// screen shows on it.
func (s *sequencer) PauseScreen(id uint32) {
	s.mu.Lock()
	s.paused[id] = true
	s.mu.Unlock()
}

// ResumeScreen lets a device's screen sequences draw again and draws the
// frame each shows now. It reports whether one did: the screen shows the
// configuration's picture rather than the device's own.
func (s *sequencer) ResumeScreen(id uint32) bool {
	prefix := fmt.Sprintf("%x/", id)
	s.mu.Lock()
	delete(s.paused, id)
	var draw []screenSeq
	for k, sc := range s.screens {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			draw = append(draw, *sc)
		}
	}
	s.mu.Unlock()
	shown := false
	for _, sc := range draw {
		if err := sc.d.WriteSequenceFrame(sc.seq, sc.frame); err != nil {
			slog.Debug("sequence frame", "device", sc.d.Name, "api", sc.seq.Api, "err", err)
			continue
		}
		shown = true
	}
	return shown
}

// showFrame records a screen sequence's frame and draws it unless the
// device's screen is paused. A play that another has replaced (stop is no
// longer key's) draws nothing.
func (s *sequencer) showFrame(key string, stop chan struct{}, d *devicert.Decl, seq *devicert.Sequence, frame map[string]any) error {
	s.mu.Lock()
	if cur, ok := s.running[key]; ok && cur != stop {
		s.mu.Unlock()
		return nil
	}
	s.screens[key] = &screenSeq{d: d, seq: seq, frame: frame}
	paused := s.paused[d.ID]
	s.mu.Unlock()
	if paused {
		return nil
	}
	return d.WriteSequenceFrame(seq, frame)
}

// start makes key's animation the one that plays, ending the one before.
// The returned channel closes when another replaces it or it is stopped.
func (s *sequencer) start(key string) chan struct{} {
	stop := make(chan struct{})
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.running[key]; ok {
		close(ch)
	}
	s.running[key] = stop
	delete(s.screens, key)
	return stop
}

// end stops key's animation if it is still the one started with stop.
func (s *sequencer) end(key string, stop chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[key] == stop {
		close(stop)
		delete(s.running, key)
	}
}

func (s *sequencer) Play(d *devicert.Decl, api string, seq *devicert.Sequence, value any) error {
	key := fmt.Sprintf("%x/%s", d.ID, api)
	// Taking the key before writing: a play that starts while this one
	// writes its first frame replaces it rather than running beside it.
	stop := s.start(key)
	m, _ := value.(map[string]any)
	frames, _ := m["data"].([]any)
	if len(frames) == 0 {
		s.end(key, stop)
		return nil
	}
	write := func(frame any) error { return d.WriteApi(api, frameValues(frame)) }
	if isScreenFrame(frames[0]) {
		write = func(frame any) error { return s.showFrame(key, stop, d, seq, frameValues(frame)) }
	}
	if allSame(frames) {
		s.end(key, stop)
		return write(frames[0])
	}
	if err := write(frames[0]); err != nil {
		s.end(key, stop)
		return err
	}
	step := time.Duration(d.SequenceStepMs) * time.Millisecond
	if step <= 0 {
		step = 33 * time.Millisecond
	}
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
			if err := write(frames[i%len(frames)]); err != nil {
				slog.Debug("sequence frame", "device", d.Name, "api", api, "err", err)
				if !d.Connected() {
					s.end(key, stop)
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
