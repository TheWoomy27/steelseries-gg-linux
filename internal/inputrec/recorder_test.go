package inputrec

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseDevices(t *testing.T) {
	const proc = `I: Bus=0019 Vendor=0000 Product=0001 Version=0000
N: Name="Power Button"
H: Handlers=kbd event2
B: EV=3

I: Bus=0003 Vendor=1038 Product=1612 Version=0111
N: Name="SteelSeries SteelSeries Apex 7"
H: Handlers=sysrq kbd leds event5
B: EV=120013

I: Bus=0003 Vendor=1038 Product=1854 Version=0111
N: Name="SteelSeries Aerox 5 Wireless"
H: Handlers=mouse2 event9
B: EV=17

I: Bus=0000 Vendor=0000 Product=0000 Version=0000
N: Name="HDA Intel PCH Headphone"
H: Handlers=event12
B: EV=21
`
	got := parseDevices(strings.NewReader(proc))
	want := []string{"/dev/input/event2", "/dev/input/event5", "/dev/input/event9"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("devices %v, want %v", got, want)
	}
}

func TestTranslate(t *testing.T) {
	for _, c := range []struct {
		e    raw
		want []Event
	}{
		{raw{typ: evKey, code: 30, value: 1}, []Event{{Type: typeKey, Page: 1, Code: 4, ExtraData: 1}}},      // A down
		{raw{typ: evKey, code: 30, value: 2}, nil},                                                           // A repeating
		{raw{typ: evKey, code: 42, value: 0}, []Event{{Type: typeKey, Page: 1, Code: 0xe1}}},                 // left shift up
		{raw{typ: evKey, code: 43, value: 1}, []Event{{Type: typeKey, Page: 1, Code: 0x31, ExtraData: 1}}},   // backslash
		{raw{typ: evKey, code: 115, value: 1}, []Event{{Type: typeKey, Page: 1, Code: 0x80, ExtraData: 1}}},  // volume up
		{raw{typ: evKey, code: 164, value: 1}, []Event{{Type: typeKey, Page: 12, Code: 0xcd, ExtraData: 1}}}, // play/pause
		{raw{typ: evKey, code: 0x111, value: 1}, []Event{{Type: typeMouseButton, Page: 1, Code: 2, ExtraData: 1}}},
		{raw{typ: evRel, code: relWheel, value: -2}, []Event{{Type: typeMouseWheel, ExtraData: -1}, {Type: typeMouseWheel, ExtraData: -1}}},
		{raw{typ: evRel, code: 0, value: 5}, nil}, // pointer motion
	} {
		if got := translate(c.e); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v: %+v, want %+v", c.e, got, c.want)
		}
	}
	// Every key the kernel maps from a keyboard usage maps back to one.
	for usage, key := range hidKeyboard {
		if key == 0 {
			continue
		}
		if u, ok := keyUsage[key]; !ok || hidKeyboard[u] != key {
			t.Errorf("key %d (usage %#x) maps back to %#x", key, usage, u)
		}
	}
}

func inputEvent(at time.Time, typ, code uint16, value int32) []byte {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint64(b[0:], uint64(at.Unix()))
	binary.LittleEndian.PutUint64(b[8:], uint64(at.Nanosecond()/1000))
	binary.LittleEndian.PutUint16(b[16:], typ)
	binary.LittleEndian.PutUint16(b[18:], code)
	binary.LittleEndian.PutUint32(b[20:], uint32(value))
	return b
}

// TestRecord records from a FIFO standing in for a keyboard: A pressed
// and released 40 ms later, with the SYN reports between them skipped.
func TestRecord(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "event0")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no FIFOs:", err)
	}
	at := time.Now()
	go func() {
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer w.Close()
		for _, b := range [][]byte{
			inputEvent(at, evKey, 30, 1), inputEvent(at, 0, 0, 0),
			inputEvent(at.Add(40*time.Millisecond), evKey, 30, 0), inputEvent(at.Add(40*time.Millisecond), 0, 0, 0),
		} {
			w.Write(b)
		}
		time.Sleep(time.Second)
	}()
	got := make(chan Event, 8)
	r := &Recorder{devices: func() ([]string, error) { return []string{fifo}, nil }}
	if err := r.Start(time.Minute, func(e Event) { got <- e }, nil); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	var events []Event
	for len(events) < 3 {
		select {
		case e := <-got:
			e.Timestamp = 0
			events = append(events, e)
		case <-time.After(2 * time.Second):
			t.Fatalf("recorded %+v", events)
		}
	}
	want := []Event{
		{Type: typeKey, Page: 1, Code: 4, ExtraData: 1, EventNum: 1},
		{Type: typeDelay, ExtraData: 40, EventNum: 2},
		{Type: typeKey, Page: 1, Code: 4, EventNum: 3},
	}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("recorded %+v\nwant %+v", events, want)
	}
}

func TestRecordTimesOut(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "event0")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no FIFOs:", err)
	}
	go func() {
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			time.Sleep(time.Second)
			w.Close()
		}
	}()
	ended := make(chan struct{})
	r := &Recorder{devices: func() ([]string, error) { return []string{fifo}, nil }}
	if err := r.Start(50*time.Millisecond, func(Event) {}, func() { close(ended) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the recording did not time out")
	}
}
