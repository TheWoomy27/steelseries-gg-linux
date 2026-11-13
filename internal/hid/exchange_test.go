package hid

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// A reply that arrives before the request that asked for it has returned
// (a feature report's ioctl returns after the device may already have
// answered) still reaches the request, not the event handler.
func TestExchangeReplyBeforeSendReturns(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	dev := os.NewFile(uintptr(fds[1]), "device")
	defer dev.Close()
	d := &Device{Info: Info{Desc: Descriptor{Numbered: true}}, f: os.NewFile(uintptr(fds[0]), "hidraw"),
		closed: make(chan struct{}), opened: time.Now().Add(-time.Second)}
	go d.readLoop()
	defer d.Close()
	var events [][]byte
	d.OnEvent(func(r []byte) { events = append(events, r) })

	send := func(data []byte, size int) error {
		// The device answers at once; the request returns only after the
		// read loop has taken the reply.
		dev.Write([]byte{0x00, 0x21, 0x00})
		time.Sleep(50 * time.Millisecond)
		return nil
	}
	r, err := d.exchange([]byte{0x00, 0x21, 0x01}, 642, time.Second, send)
	if err != nil {
		t.Fatalf("exchange: %v (events: %x)", err, events)
	}
	if len(r) < 2 || r[1] != 0x21 {
		t.Errorf("reply % x", r)
	}
}
