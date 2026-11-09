// Package hid talks to SteelSeries devices through Linux hidraw.
//
// Reports are framed the way Windows HID APIs frame them, which is what the
// device specifications assume: the first byte of every input, output and
// feature report buffer is the report ID, 0 when the interface does not
// number its reports.
package hid

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const SteelSeriesVID = 0x1038

// Info describes one hidraw node.
type Info struct {
	Path      string // /dev/hidrawN
	Sysfs     string
	VendorID  uint16
	ProductID uint16
	Interface int
	Name      string
	Serial    string
	Desc      Descriptor
}

// Enumerate lists SteelSeries hidraw nodes.
func Enumerate() ([]Info, error) {
	entries, err := os.ReadDir("/sys/class/hidraw")
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		sys := filepath.Join("/sys/class/hidraw", e.Name())
		info, err := readInfo(sys)
		if err != nil || info.VendorID != SteelSeriesVID {
			continue
		}
		info.Path = "/dev/" + e.Name()
		out = append(out, info)
	}
	return out, nil
}

func readInfo(sys string) (Info, error) {
	info := Info{Sysfs: sys, Interface: -1}
	f, err := os.Open(filepath.Join(sys, "device", "uevent"))
	if err != nil {
		return info, err
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), "=")
		switch k {
		case "HID_ID": // bus:vendor:product
			parts := strings.Split(v, ":")
			if len(parts) == 3 {
				vid, _ := strconv.ParseUint(parts[1], 16, 32)
				pid, _ := strconv.ParseUint(parts[2], 16, 32)
				info.VendorID, info.ProductID = uint16(vid), uint16(pid)
			}
		case "HID_NAME":
			info.Name = v
		case "HID_UNIQ":
			info.Serial = v
		}
	}
	f.Close()
	// .../X-Y:1.N/0003:1038:1852.0004 -> interface N
	if dev, err := filepath.EvalSymlinks(filepath.Join(sys, "device")); err == nil {
		if b, err := os.ReadFile(filepath.Join(filepath.Dir(dev), "bInterfaceNumber")); err == nil {
			if n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 16, 8); err == nil {
				info.Interface = int(n)
			}
		}
	}
	if rd, err := os.ReadFile(filepath.Join(sys, "device", "report_descriptor")); err == nil {
		info.Desc = ParseDescriptor(rd)
	}
	return info, nil
}

// Device is an open hidraw node. A single reader goroutine owns the read
// side: replies go to an in-flight request, everything else to Events.
type Device struct {
	Info
	f       *os.File
	reqMu   sync.Mutex // one write or request/response exchange at a time
	sent    time.Time  // when the last write that did not wait for its reply went out (under reqMu)
	mu      sync.Mutex
	waiting chan []byte
	events  func([]byte)
	last    []byte    // most recent input report, when it arrived and how
	lastAt  time.Time // many times in a row it has repeated
	repeats int
	opened  time.Time
	closed  chan struct{}
}

var ErrTimeout = errors.New("hid: timed out waiting for report")

// traceReports logs every input report (GG_LINUX_HID_TRACE=1).
var traceReports = os.Getenv("GG_LINUX_HID_TRACE") != ""

// Open opens a hidraw node read/write.
func Open(info Info) (*Device, error) {
	fd, err := syscall.Open(info.Path, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", info.Path, err)
	}
	d := &Device{Info: info, f: os.NewFile(uintptr(fd), info.Path), closed: make(chan struct{}), opened: time.Now()}
	go d.readLoop()
	return d, nil
}

func (d *Device) Close() error {
	select {
	case <-d.closed:
	default:
		close(d.closed)
	}
	return d.f.Close()
}

// OnEvent installs a callback for input reports that are not replies.
func (d *Device) OnEvent(fn func([]byte)) {
	d.mu.Lock()
	d.events = fn
	d.mu.Unlock()
}

func (d *Device) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := d.f.Read(buf)
		if err != nil {
			return
		}
		r := make([]byte, 0, n+1)
		if !d.Desc.Numbered {
			r = append(r, 0)
		}
		r = append(r, buf[:n]...)
		if traceReports {
			slog.Debug("hid report", "pid", fmt.Sprintf("%04x", d.ProductID), "if", d.Interface, "data", fmt.Sprintf("% x", r[:min(len(r), 12)]))
		}
		d.mu.Lock()
		w, ev := d.waiting, d.events
		if bytes.Equal(r, d.last) {
			d.repeats++
		} else {
			d.repeats = 1
		}
		d.last, d.lastAt = r, time.Now()
		d.mu.Unlock()
		delivered := false
		if w != nil {
			select {
			case w <- r:
				delivered = true
			default:
			}
		}
		if !delivered && ev != nil {
			ev(r)
		}
	}
}

func (d *Device) outputLen(id uint8, fallback int) int {
	if n, ok := d.Desc.Output[id]; ok && n > 0 {
		return n + 1
	}
	return fallback
}

func (d *Device) featureLen(id uint8, fallback int) int {
	if n, ok := d.Desc.Feature[id]; ok && n > 0 {
		return n + 1
	}
	return fallback
}

func pad(data []byte, n int) []byte {
	if n <= 0 {
		n = len(data)
	}
	b := make([]byte, n)
	copy(b, data)
	if len(b) == 0 {
		b = []byte{0}
	}
	return b
}

// replySettle is how long a request waits after a write that did not wait
// for its reply. A dongle tunnels one command at a time to its wireless
// device; a request sent while the previous write is still on its way is
// dropped, so its reply never comes. A lighting animation writes a frame
// every 15 ms, and a round trip to an Aerox mouse takes about 7.
const replySettle = 20 * time.Millisecond

// Write sends an output report (buf[0] = report ID). It waits for an
// exchange in progress, so its reply is not lost.
func (d *Device) Write(data []byte, size int) error {
	d.reqMu.Lock()
	defer d.reqMu.Unlock()
	d.sent = time.Now()
	return d.write(data, size)
}

func (d *Device) write(data []byte, size int) error {
	b := pad(data, size)
	b = b[:min(len(b), d.outputLen(b[0], len(b)))]
	if traceReports {
		slog.Debug("hid write", "pid", fmt.Sprintf("%04x", d.ProductID), "if", d.Interface, "data", fmt.Sprintf("% x", b[:min(len(b), 12)]))
	}
	_, err := d.f.Write(b)
	return err
}

// Exchange writes an output report and waits for its reply.
//
// Command interfaces also carry reports the device pushes on its own; some
// dongles even repeat the same report continuously. A reply is therefore
// the first report that echoes the request's command byte (dongles set flag
// bits on tunnelled replies, so only the low six bits are compared) or that
// differs from the report the interface was repeating just before the
// request. Repeats of that background report are passed to OnEvent.
func (d *Device) Exchange(data []byte, size int, timeout time.Duration) ([]byte, error) {
	d.reqMu.Lock()
	defer d.reqMu.Unlock()
	// Right after opening, give a streaming interface a moment so its
	// background report is known before the request goes out.
	if wait := 150*time.Millisecond - time.Since(d.opened); wait > 0 {
		time.Sleep(wait)
	}
	if wait := replySettle - time.Since(d.sent); wait > 0 {
		time.Sleep(wait)
	}
	ch := make(chan []byte, 64)
	d.mu.Lock()
	d.waiting = ch
	var background []byte
	if d.repeats >= 2 && time.Since(d.lastAt) < 100*time.Millisecond {
		background = d.last
	}
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		if d.waiting == ch {
			d.waiting = nil
		}
		d.mu.Unlock()
	}()
	if err := d.write(data, size); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	echoes := func(r []byte) bool { return len(data) >= 2 && len(r) >= 2 && r[1]&0x3f == data[1]&0x3f }
	deadline := time.After(timeout)
	for {
		select {
		case r := <-ch:
			if echoes(r) || background == nil || !bytes.Equal(r, background) {
				return r, nil
			}
			d.mu.Lock()
			ev := d.events
			d.mu.Unlock()
			if ev != nil {
				ev(r)
			}
		case <-deadline:
			return nil, ErrTimeout
		case <-d.closed:
			return nil, os.ErrClosed
		}
	}
}

// ReadInput waits for the next input report without writing.
func (d *Device) ReadInput(timeout time.Duration) ([]byte, error) {
	d.reqMu.Lock()
	defer d.reqMu.Unlock()
	ch := make(chan []byte, 1)
	d.mu.Lock()
	d.waiting = ch
	d.mu.Unlock()
	return d.await(ch, timeout)
}

func (d *Device) await(ch chan []byte, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	select {
	case r := <-ch:
		return r, nil
	case <-time.After(timeout):
		d.mu.Lock()
		if d.waiting == ch {
			d.waiting = nil
		}
		d.mu.Unlock()
		return nil, ErrTimeout
	case <-d.closed:
		return nil, os.ErrClosed
	}
}

// hidraw ioctls (linux/hidraw.h).
func iocRW(nr, size uintptr) uintptr {
	const dirRW = 3
	return dirRW<<30 | size<<16 | 'H'<<8 | nr
}

func (d *Device) ioctl(req uintptr, buf []byte) (int, error) {
	raw, err := d.f.SyscallConn()
	if err != nil {
		return 0, err
	}
	var n uintptr
	var errno syscall.Errno
	cerr := raw.Control(func(fd uintptr) {
		n, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&buf[0])))
	})
	if cerr != nil {
		return 0, cerr
	}
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

// SetFeature sends a feature report (buf[0] = report ID).
func (d *Device) SetFeature(data []byte, size int) error {
	b := pad(data, size)
	b = b[:min(len(b), d.featureLen(b[0], len(b)))]
	_, err := d.ioctl(iocRW(0x06, uintptr(len(b))), b)
	return err
}

// GetFeature reads a feature report; data[0] selects the report ID.
func (d *Device) GetFeature(id uint8, size int) ([]byte, error) {
	n := d.featureLen(id, size)
	if n < 2 {
		n = max(size, 2)
	}
	return d.getReport(0x07, id, n)
}

// GetInputReport asks for an input report over the control pipe, as
// HidD_GetInputReport does, instead of waiting for the device to send one.
func (d *Device) GetInputReport(id uint8, size int) ([]byte, error) {
	n := size
	if l, ok := d.Desc.Input[id]; ok && l > 0 {
		n = l + 1
	}
	return d.getReport(0x0A, id, max(n, 2))
}

func (d *Device) getReport(nr uintptr, id uint8, n int) ([]byte, error) {
	b := make([]byte, n)
	b[0] = id
	got, err := d.ioctl(iocRW(nr, uintptr(len(b))), b)
	if err != nil {
		return nil, err
	}
	return b[:got], nil
}
