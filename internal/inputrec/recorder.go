// Package inputrec records keyboard and mouse input for GG's macro editor
// and its key fields (key bindings, shortcuts), as GG's core does on
// Windows with input hooks: each key or button event becomes a GG device
// event, preceded by a delay event holding the time since the one before.
//
// It reads /dev/input (the user has to be in the input group). Devices
// are read, never grabbed, so typing reaches the desktop as usual.
package inputrec

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// Event is a recorded event (deviceinfo.DeviceEventRecording).
type Event struct {
	Type      uint8  `json:"type"`
	Page      uint8  `json:"page"`
	Code      uint16 `json:"code"`
	ExtraData int64  `json:"extraData"`
	Timestamp uint32 `json:"timestamp"` // ms since the recording started
	EventNum  uint32 `json:"eventNum"`
}

// GG event types.
const (
	typeMouseButton = 0
	typeMouseWheel  = 1
	typeKey         = 2
	typeDelay       = 4
	typeACPan       = 12
)

// Recorder records one session at a time.
type Recorder struct {
	mu  sync.Mutex
	cur *session

	devices func() ([]string, error) // the event devices to read
}

type session struct {
	files []*os.File
	done  chan struct{}
	once  sync.Once
}

func (s *session) stop() {
	s.once.Do(func() {
		close(s.done)
		for _, f := range s.files {
			f.Close()
		}
	})
}

// raw is a Linux input_event.
type raw struct {
	at        time.Time
	typ, code uint16
	value     int32
}

// Start records until Stop or timeout (ending a recording already
// running). emit gets each event; ended runs when the recording stops on
// its own.
func (r *Recorder) Start(timeout time.Duration, emit func(Event), ended func()) error {
	r.Stop()
	list := r.devices
	if list == nil {
		list = inputDevices
	}
	paths, err := list()
	if err != nil {
		return err
	}
	s := &session{done: make(chan struct{})}
	events := make(chan raw, 64)
	var denied error
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			if denied == nil {
				denied = err
			}
			continue
		}
		s.files = append(s.files, f)
		go read(f, events, s.done)
	}
	if len(s.files) == 0 {
		if denied != nil {
			return fmt.Errorf("no keyboard or mouse can be read (is the user in the input group?): %w", denied)
		}
		return errors.New("no keyboard or mouse found")
	}
	r.mu.Lock()
	r.cur = s
	r.mu.Unlock()
	slog.Info("recording input", "devices", len(s.files), "timeout", timeout)
	go func() {
		start := time.Now()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		var last time.Time
		var n uint32
		send := func(ev Event, at time.Time) {
			if !last.IsZero() {
				if d := at.Sub(last).Round(time.Millisecond).Milliseconds(); d > 0 {
					n++
					emit(Event{Type: typeDelay, ExtraData: d, Timestamp: stamp(at, start), EventNum: n})
				}
			}
			last = at
			n++
			ev.Timestamp, ev.EventNum = stamp(at, start), n
			emit(ev)
		}
		for {
			select {
			case <-s.done:
				return
			case <-timer.C:
				r.end(s)
				if ended != nil {
					ended()
				}
				return
			case e := <-events:
				for _, ev := range translate(e) {
					send(ev, e.at)
				}
			}
		}
	}()
	return nil
}

// Stop ends the recording, if one is running.
func (r *Recorder) Stop() {
	r.mu.Lock()
	s := r.cur
	r.cur = nil
	r.mu.Unlock()
	if s != nil {
		s.stop()
		slog.Info("input recording stopped")
	}
}

func (r *Recorder) end(s *session) {
	r.mu.Lock()
	if r.cur == s {
		r.cur = nil
	}
	r.mu.Unlock()
	s.stop()
	slog.Info("input recording timed out")
}

func stamp(at, start time.Time) uint32 {
	if d := at.Sub(start).Milliseconds(); d > 0 {
		return uint32(d)
	}
	return 0
}

// read passes a device's events on until it is closed.
func read(f *os.File, out chan<- raw, done <-chan struct{}) {
	buf := make([]byte, 24*16)
	for {
		n, err := f.Read(buf)
		if err != nil {
			return
		}
		for i := 0; i+24 <= n; i += 24 {
			b := buf[i : i+24]
			e := raw{
				at:    time.Unix(int64(binary.LittleEndian.Uint64(b[0:])), int64(binary.LittleEndian.Uint64(b[8:]))*1000),
				typ:   binary.LittleEndian.Uint16(b[16:]),
				code:  binary.LittleEndian.Uint16(b[18:]),
				value: int32(binary.LittleEndian.Uint32(b[20:])),
			}
			if e.typ != evKey && e.typ != evRel {
				continue
			}
			select {
			case out <- e:
			case <-done:
				return
			}
		}
	}
}

// Linux input event types and relative axes.
const (
	evKey     = 1
	evRel     = 2
	relHWheel = 6
	relWheel  = 8
)

// translate turns a Linux input event into GG's events (none for events
// GG doesn't record, such as key repeats and pointer motion).
func translate(e raw) []Event {
	switch e.typ {
	case evKey:
		if e.value != 0 && e.value != 1 {
			return nil // auto-repeat
		}
		down := int64(e.value)
		if b, ok := mouseButtons[e.code]; ok {
			return []Event{{Type: typeMouseButton, Page: 1, Code: b, ExtraData: down}}
		}
		if u, ok := consumerKeys[e.code]; ok {
			return []Event{{Type: typeKey, Page: 12, Code: u, ExtraData: down}}
		}
		if u, ok := keyUsage[e.code]; ok {
			return []Event{{Type: typeKey, Page: 1, Code: uint16(u), ExtraData: down}}
		}
	case evRel:
		typ := uint8(typeMouseWheel)
		switch e.code {
		case relWheel:
		case relHWheel:
			typ = typeACPan
		default:
			return nil
		}
		dir, steps := int64(1), int(e.value)
		if steps < 0 {
			dir, steps = -1, -steps
		}
		var out []Event
		for i := 0; i < min(steps, 10); i++ {
			out = append(out, Event{Type: typ, ExtraData: dir})
		}
		return out
	}
	return nil
}

// inputDevices lists the event devices of keyboards and mice, from
// /proc/bus/input/devices.
func inputDevices() ([]string, error) {
	f, err := os.Open("/proc/bus/input/devices")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseDevices(f), nil
}

func parseDevices(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "H: Handlers=") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "H: Handlers="))
		input, event := false, ""
		for _, h := range fields {
			switch {
			case h == "kbd" || strings.HasPrefix(h, "mouse"):
				input = true
			case strings.HasPrefix(h, "event"):
				event = h
			}
		}
		if input && event != "" {
			out = append(out, "/dev/input/"+event)
		}
	}
	return out
}
