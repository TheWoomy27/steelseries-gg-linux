package vinput

import (
	"bufio"
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveKey makes the virtual device and reads back a press of F24
// (which desktops leave alone) from its event node. It needs
// /dev/uinput, so it only runs with GG_LINUX_VINPUT_LIVE set.
func TestLiveKey(t *testing.T) {
	if os.Getenv("GG_LINUX_VINPUT_LIVE") == "" {
		t.Skip("GG_LINUX_VINPUT_LIVE not set")
	}
	const name = "gg-linux vinput test"
	d, err := Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var node string
	for i := 0; i < 50 && node == ""; i++ {
		time.Sleep(20 * time.Millisecond)
		f, _ := os.Open("/proc/bus/input/devices")
		sc := bufio.NewScanner(f)
		mine := false
		for sc.Scan() {
			l := sc.Text()
			if strings.HasPrefix(l, "N: Name=") {
				mine = strings.Contains(l, name)
			}
			if mine && strings.HasPrefix(l, "H: Handlers=") {
				for _, h := range strings.Fields(l) {
					if strings.HasPrefix(h, "event") {
						node = "/dev/input/" + h
					}
				}
			}
		}
		f.Close()
	}
	if node == "" {
		t.Fatal("the virtual device has no event node")
	}
	// udev gives the node its group a moment after it appears.
	var ev *os.File
	for i := 0; i < 50; i++ {
		if ev, err = os.Open(node); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	got := make(chan [2]int32, 8)
	go func() {
		b := make([]byte, 24)
		for {
			if _, err := ev.Read(b); err != nil {
				return
			}
			if typ := binary.LittleEndian.Uint16(b[16:]); typ == evKey {
				got <- [2]int32{int32(binary.LittleEndian.Uint16(b[18:])), int32(binary.LittleEndian.Uint32(b[20:]))}
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	const f24 = 194
	d.Key(f24, true)
	d.Key(f24, false)
	for _, want := range [][2]int32{{f24, 1}, {f24, 0}} {
		select {
		case g := <-got:
			if g != want {
				t.Errorf("read %v, want %v", g, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("no %v", want)
		}
	}
}
