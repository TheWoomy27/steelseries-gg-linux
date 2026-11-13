package mousefilter

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestTurn(t *testing.T) {
	f := &Filter{angle: 90}
	n := &node{f: f}
	// Clockwise on screen: right becomes down.
	if x, y := n.turn(10, 0); x != 0 || y != 10 {
		t.Errorf("90°: (10, 0) -> (%d, %d), want (0, 10)", x, y)
	}
	// Small movements add up instead of rounding away.
	f.SetAngle(30)
	n = &node{f: f}
	var sx, sy int32
	for i := 0; i < 100; i++ {
		x, y := n.turn(1, 0)
		sx, sy = sx+x, sy+y
	}
	if wx, wy := 100*math.Cos(math.Pi/6), 100*math.Sin(math.Pi/6); math.Abs(float64(sx)-wx) > 1 || math.Abs(float64(sy)-wy) > 1 {
		t.Errorf("30° x100: (%d, %d), want about (%.1f, %.1f)", sx, sy, wx, wy)
	}
}

// TestFilterOnUinput runs the filter on a virtual mouse. It needs
// /dev/uinput and read access to /dev/input, so it runs only with
// GG_TEST_UINPUT=1. The filter's copy is grabbed here, so the desktop's
// pointer does not move.
func TestFilterOnUinput(t *testing.T) {
	if os.Getenv("GG_TEST_UINPUT") == "" {
		t.Skip("set GG_TEST_UINPUT=1 to run on /dev/uinput")
	}
	const name = "gg-linux mousefilter test"
	src, err := os.OpenFile("/dev/uinput", os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Skip(err)
	}
	defer src.Close()
	for _, c := range []struct {
		req uintptr
		v   int
	}{{uiSetEvBit, evKey}, {uiSetKeyBit, 0x110}, {uiSetEvBit, evRel}, {uiSetRelBit, relX}, {uiSetRelBit, relY}} {
		if err := ioctl(src, c.req, c.v); err != nil {
			t.Fatal(err)
		}
	}
	setup := make([]byte, 92)
	binary.LittleEndian.PutUint16(setup[0:], 0x03)
	binary.LittleEndian.PutUint16(setup[2:], 0x1038)
	binary.LittleEndian.PutUint16(setup[4:], 0xfff0)
	copy(setup[8:], name)
	if err := ioctlPtr(src, uiDevSetup, unsafe.Pointer(&setup[0])); err != nil {
		t.Fatal(err)
	}
	if err := ioctl(src, uiDevCreate, 0); err != nil {
		t.Fatal(err)
	}
	srcNode := waitNode(t, name, "")

	// udev gives new nodes their group a moment after they appear.
	var f *Filter
	retry(t, func() (err error) { f, err = Start([]string{srcNode}, 90); return })
	defer f.Close()
	outNode := waitNode(t, name, srcNode)
	var out *os.File
	retry(t, func() (err error) { out, err = os.OpenFile(outNode, os.O_RDONLY, 0); return })
	defer out.Close()
	if err := ioctl(out, eviocgrab, 1); err != nil {
		t.Fatal(err)
	}

	send := func(evs ...[3]int32) {
		var b []byte
		for _, e := range evs {
			b = appendEvent(b, uint16(e[0]), uint16(e[1]), e[2])
		}
		if _, err := src.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	// Right 10 with the button down comes out as down 10.
	send([3]int32{evKey, 0x110, 1}, [3]int32{evRel, relX, 10}, [3]int32{evSyn, synReport, 0})
	send([3]int32{evKey, 0x110, 0}, [3]int32{evSyn, synReport, 0})
	got := map[[2]uint16]int32{}
	buf := make([]byte, 16*eventSize)
	deadline := time.Now().Add(2 * time.Second)
	events := 0
	for events < 5 && time.Now().Before(deadline) {
		out.SetReadDeadline(deadline)
		k, err := out.Read(buf)
		if err != nil {
			break
		}
		for off := 0; off+eventSize <= k; off += eventSize {
			ev := buf[off : off+eventSize]
			h := len(ev) - 8
			typ, code := binary.LittleEndian.Uint16(ev[h:]), binary.LittleEndian.Uint16(ev[h+2:])
			val := int32(binary.LittleEndian.Uint32(ev[h+4:]))
			got[[2]uint16{typ, code}] += val
			events++
		}
	}
	if got[[2]uint16{evRel, relX}] != 0 || got[[2]uint16{evRel, relY}] != 10 {
		t.Errorf("motion out: x %d y %d, want x 0 y 10", got[[2]uint16{evRel, relX}], got[[2]uint16{evRel, relY}])
	}
	if events != 5 { // key down, rel y, syn, key up, syn
		t.Errorf("got %d events, want 5", events)
	}
}

func retry(t *testing.T, fn func() error) {
	t.Helper()
	var err error
	for i := 0; i < 50; i++ {
		if err = fn(); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(err)
}

// waitNode finds the event node of the input device named name, other
// than skip.
func waitNode(t *testing.T, name, skip string) string {
	t.Helper()
	for i := 0; i < 100; i++ {
		paths, _ := filepath.Glob("/sys/class/input/event*")
		for _, p := range paths {
			b, _ := os.ReadFile(p + "/device/name")
			node := "/dev/input/" + filepath.Base(p)
			if strings.TrimSpace(string(b)) == name && node != skip {
				return node
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no event node for %q", name)
	return ""
}
