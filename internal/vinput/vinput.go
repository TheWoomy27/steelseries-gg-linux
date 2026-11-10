// Package vinput is a virtual keyboard and mouse (Linux uinput) that ggd
// plays macros through, as GG sends input on Windows. The user needs
// write access to /dev/uinput (the input group on most systems).
package vinput

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Linux input event types and codes.
const (
	evSyn     = 0
	evKey     = 1
	evRel     = 2
	relX      = 0
	relY      = 1
	relHWheel = 6
	relWheel  = 8

	uiSetEvBit   = 0x40045564
	uiSetKeyBit  = 0x40045565
	uiSetRelBit  = 0x40045566
	uiDevCreate  = 0x5501
	uiDevDestroy = 0x5502
)

// Device is a virtual keyboard and mouse.
type Device struct {
	mu   sync.Mutex
	f    *os.File
	held map[uint16]bool
}

// Open creates the virtual device, named name.
func Open(name string) (*Device, error) {
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("virtual input: %w", err)
	}
	fd := int(f.Fd())
	ioctl := func(req, v uintptr) error { return unix.IoctlSetInt(fd, uint(req), int(v)) }
	errs := []error{ioctl(uiSetEvBit, evKey), ioctl(uiSetEvBit, evRel)}
	for k := uintptr(1); k < 0x100; k++ {
		errs = append(errs, ioctl(uiSetKeyBit, k))
	}
	for b := uintptr(0x110); b <= 0x117; b++ { // BTN_LEFT .. BTN_TASK
		errs = append(errs, ioctl(uiSetKeyBit, b))
	}
	for _, r := range []uintptr{relX, relY, relHWheel, relWheel} {
		errs = append(errs, ioctl(uiSetRelBit, r))
	}
	for _, e := range errs {
		if e != nil {
			f.Close()
			return nil, fmt.Errorf("virtual input setup: %w", e)
		}
	}
	// struct uinput_user_dev: name, input_id (USB), ff_effects_max, abs*.
	setup := make([]byte, 80+8+4+4*64*4)
	copy(setup, name)
	binary.LittleEndian.PutUint16(setup[80:], 0x03)
	binary.LittleEndian.PutUint16(setup[82:], 0x1038)
	binary.LittleEndian.PutUint16(setup[84:], 0x0001)
	binary.LittleEndian.PutUint16(setup[86:], 1)
	if _, err := f.Write(setup); err != nil {
		f.Close()
		return nil, fmt.Errorf("virtual input setup: %w", err)
	}
	if err := unix.IoctlSetInt(fd, uiDevCreate, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("virtual input create: %w", err)
	}
	return &Device{f: f, held: map[uint16]bool{}}, nil
}

func (d *Device) write(typ, code uint16, value int32) error {
	b := make([]byte, 24)
	now := time.Now()
	binary.LittleEndian.PutUint64(b[0:], uint64(now.Unix()))
	binary.LittleEndian.PutUint64(b[8:], uint64(now.Nanosecond()/1000))
	binary.LittleEndian.PutUint16(b[16:], typ)
	binary.LittleEndian.PutUint16(b[18:], code)
	binary.LittleEndian.PutUint32(b[20:], uint32(value))
	_, err := d.f.Write(b)
	return err
}

func (d *Device) sync() error { return d.write(evSyn, 0, 0) }

// Key presses or releases a key or button (a Linux key code).
func (d *Device) Key(code uint16, down bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f == nil {
		return os.ErrClosed
	}
	v := int32(0)
	if down {
		v = 1
	}
	if err := d.write(evKey, code, v); err != nil {
		return err
	}
	if down {
		d.held[code] = true
	} else {
		delete(d.held, code)
	}
	return d.sync()
}

// Wheel scrolls (vertical, or horizontal when h) by n notches.
func (d *Device) Wheel(n int32, h bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f == nil {
		return os.ErrClosed
	}
	axis := uint16(relWheel)
	if h {
		axis = relHWheel
	}
	if err := d.write(evRel, axis, n); err != nil {
		return err
	}
	return d.sync()
}

// ReleaseAll lets go of every key and button still held.
func (d *Device) ReleaseAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f == nil {
		return
	}
	for k := range d.held {
		d.write(evKey, k, 0)
	}
	d.held = map[uint16]bool{}
	d.sync()
}

// Close releases what is held and removes the device.
func (d *Device) Close() {
	d.ReleaseAll()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f != nil {
		unix.IoctlSetInt(int(d.f.Fd()), uiDevDestroy, 0)
		d.f.Close()
		d.f = nil
	}
}
