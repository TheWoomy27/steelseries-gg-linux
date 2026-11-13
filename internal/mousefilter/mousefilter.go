// Package mousefilter rotates a mouse's motion, as GG's Windows driver
// does for the mice whose rotation setting says "GG required". It takes
// the mouse's input devices for itself (EVIOCGRAB) and sends their events
// on through a virtual copy (uinput) with the motion turned by an angle.
//
// The user needs read access to the mouse's /dev/input/event* nodes and
// write access to /dev/uinput (packaging/70-steelseries-gg.rules gives
// both to the logged-in user). Closing the filter, or ggd exiting, gives
// the mouse back.
package mousefilter

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02
	evMsc = 0x04

	synReport = 0
	relX      = 0x00
	relY      = 0x01

	keyMax  = 0x2ff
	relMax  = 0x0f
	mscMax  = 0x07
	propMax = 0x1f

	iocRead  = 2
	iocWrite = 1
)

func ioc(dir, typ, nr, size uintptr) uintptr { return dir<<30 | size<<16 | typ<<8 | nr }

var (
	eviocgrab   = ioc(iocWrite, 'E', 0x90, 4)
	eviocgid    = ioc(iocRead, 'E', 0x02, 8)
	uiSetEvBit  = ioc(iocWrite, 'U', 100, 4)
	uiSetKeyBit = ioc(iocWrite, 'U', 101, 4)
	uiSetRelBit = ioc(iocWrite, 'U', 102, 4)
	uiSetMscBit = ioc(iocWrite, 'U', 104, 4)
	uiSetProp   = ioc(iocWrite, 'U', 110, 4)
	uiDevSetup  = ioc(iocWrite, 'U', 3, 92)
	uiDevCreate = ioc(0, 'U', 1, 0)
)

func eviocgname(n uintptr) uintptr    { return ioc(iocRead, 'E', 0x06, n) }
func eviocgprop(n uintptr) uintptr    { return ioc(iocRead, 'E', 0x09, n) }
func eviocgbit(ev, n uintptr) uintptr { return ioc(iocRead, 'E', 0x20+ev, n) }

// eventSize is struct input_event's size: a timeval (two longs), then
// type, code and value.
var eventSize = 2*(strconv.IntSize/8) + 8

// Filter rotates the motion of one USB device's mice.
type Filter struct {
	mu    sync.Mutex
	angle float64 // degrees, clockwise on screen
	nodes []*node
}

// Nodes lists the event devices of a USB device (vendor, product) that
// move a pointer, leaving out virtual ones (this package's own copies).
func Nodes(vendor, product uint16) []string {
	paths, _ := filepath.Glob("/sys/class/input/event*")
	var out []string
	for _, p := range paths {
		real, err := filepath.EvalSymlinks(p)
		if err != nil || strings.HasPrefix(real, "/sys/devices/virtual/") {
			continue
		}
		if hexFile(p+"/device/id/vendor") != uint64(vendor) || hexFile(p+"/device/id/product") != uint64(product) {
			continue
		}
		if hexFile(p+"/device/capabilities/rel")&3 != 3 { // REL_X and REL_Y
			continue
		}
		out = append(out, "/dev/input/"+filepath.Base(p))
	}
	return out
}

func hexFile(path string) uint64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	// Capability masks are words, highest first; the last holds bit 0.
	v, _ := strconv.ParseUint(f[len(f)-1], 16, 64)
	return v
}

// Start rotates the motion of the given event devices by angle degrees.
func Start(devices []string, angle float64) (*Filter, error) {
	if len(devices) == 0 {
		return nil, errors.New("no pointer devices found")
	}
	f := &Filter{angle: angle}
	for _, dev := range devices {
		n, err := open(dev, f)
		if err != nil {
			f.Close()
			return nil, err
		}
		f.nodes = append(f.nodes, n)
	}
	for _, n := range f.nodes {
		go n.run()
	}
	return f, nil
}

// SetAngle changes the rotation.
func (f *Filter) SetAngle(angle float64) {
	f.mu.Lock()
	f.angle = angle
	f.mu.Unlock()
}

func (f *Filter) rotation() (sin, cos float64) {
	f.mu.Lock()
	a := f.angle
	f.mu.Unlock()
	return math.Sincos(a * math.Pi / 180)
}

// Close gives the mouse back.
func (f *Filter) Close() {
	for _, n := range f.nodes {
		n.close()
	}
}

// node is one grabbed event device and its virtual copy.
type node struct {
	f        *Filter
	in, out  *os.File
	once     sync.Once
	fx, fy   float64 // motion left over from rounding
	dx, dy   int32
	pending  []byte
	moved    bool
	readBuf  []byte
	writeBuf []byte
}

func open(dev string, f *Filter) (*node, error) {
	in, err := os.OpenFile(dev, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("mouse %s: %w", dev, err)
	}
	out, err := clone(in)
	if err != nil {
		in.Close()
		return nil, err
	}
	if err := ioctl(in, eviocgrab, 1); err != nil {
		in.Close()
		out.Close()
		return nil, fmt.Errorf("mouse %s: grab: %w", dev, err)
	}
	return &node{f: f, in: in, out: out, readBuf: make([]byte, 64*eventSize)}, nil
}

// clone makes a virtual device with the same name, ids and events as in,
// so desktop settings that follow the mouse still apply to it.
func clone(in *os.File) (*os.File, error) {
	out, err := os.OpenFile("/dev/uinput", os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("virtual mouse: %w", err)
	}
	fail := func(err error) (*os.File, error) {
		out.Close()
		return nil, fmt.Errorf("virtual mouse: %w", err)
	}
	copyBits := func(ev, max, set uintptr) error {
		bits := make([]byte, max/8+1)
		if err := ioctlPtr(in, eviocgbit(ev, uintptr(len(bits))), unsafe.Pointer(&bits[0])); err != nil {
			return err
		}
		any := false
		for i := uintptr(0); i <= max; i++ {
			if bits[i/8]&(1<<(i%8)) != 0 {
				if err := ioctl(out, set, int(i)); err != nil {
					return err
				}
				any = true
			}
		}
		if any {
			return ioctl(out, uiSetEvBit, int(ev))
		}
		return nil
	}
	for _, c := range [][3]uintptr{{evKey, keyMax, uiSetKeyBit}, {evRel, relMax, uiSetRelBit}, {evMsc, mscMax, uiSetMscBit}} {
		if err := copyBits(c[0], c[1], c[2]); err != nil {
			return fail(err)
		}
	}
	props := make([]byte, propMax/8+1)
	if ioctlPtr(in, eviocgprop(uintptr(len(props))), unsafe.Pointer(&props[0])) == nil {
		for i := 0; i <= propMax; i++ {
			if props[i/8]&(1<<(i%8)) != 0 {
				ioctl(out, uiSetProp, i)
			}
		}
	}
	// struct uinput_setup: input_id (bustype, vendor, product, version),
	// name[80], ff_effects_max.
	setup := make([]byte, 92)
	ioctlPtr(in, eviocgid, unsafe.Pointer(&setup[0]))
	name := make([]byte, 80)
	ioctlPtr(in, eviocgname(uintptr(len(name)-1)), unsafe.Pointer(&name[0]))
	copy(setup[8:88], name)
	if err := ioctlPtr(out, uiDevSetup, unsafe.Pointer(&setup[0])); err != nil {
		return fail(err)
	}
	if err := ioctl(out, uiDevCreate, 0); err != nil {
		return fail(err)
	}
	return out, nil
}

func (n *node) close() {
	n.once.Do(func() {
		n.in.Close() // releases the grab
		n.out.Close()
	})
}

func (n *node) run() {
	defer n.close()
	for {
		k, err := n.in.Read(n.readBuf)
		if err != nil {
			return // closed, or the mouse was unplugged
		}
		for off := 0; off+eventSize <= k; off += eventSize {
			n.event(n.readBuf[off : off+eventSize])
		}
	}
}

// event handles one input_event: motion is held until the report ends,
// then sent turned; everything else passes through in order.
func (n *node) event(ev []byte) {
	h := len(ev) - 8
	typ := binary.LittleEndian.Uint16(ev[h:])
	code := binary.LittleEndian.Uint16(ev[h+2:])
	val := int32(binary.LittleEndian.Uint32(ev[h+4:]))
	switch {
	case typ == evRel && code == relX:
		n.dx += val
		n.moved = true
		return
	case typ == evRel && code == relY:
		n.dy += val
		n.moved = true
		return
	case typ == evSyn && code == synReport:
		if n.moved {
			x, y := n.turn(n.dx, n.dy)
			if x != 0 {
				n.pending = appendEvent(n.pending, evRel, relX, x)
			}
			if y != 0 {
				n.pending = appendEvent(n.pending, evRel, relY, y)
			}
			n.dx, n.dy, n.moved = 0, 0, false
		}
		n.pending = appendEvent(n.pending, evSyn, synReport, 0)
		n.out.Write(n.pending)
		n.pending = n.pending[:0]
		return
	}
	n.pending = appendEvent(n.pending, typ, code, val)
}

// turn rotates a movement clockwise on screen (y grows downwards), keeping
// the fractions rounding leaves for the next one.
func (n *node) turn(dx, dy int32) (int32, int32) {
	sin, cos := n.f.rotation()
	x := float64(dx)*cos - float64(dy)*sin + n.fx
	y := float64(dx)*sin + float64(dy)*cos + n.fy
	rx, ry := math.Round(x), math.Round(y)
	n.fx, n.fy = x-rx, y-ry
	return int32(rx), int32(ry)
}

func appendEvent(b []byte, typ, code uint16, val int32) []byte {
	b = append(b, make([]byte, eventSize-8)...) // the kernel stamps the time
	b = binary.LittleEndian.AppendUint16(b, typ)
	b = binary.LittleEndian.AppendUint16(b, code)
	return binary.LittleEndian.AppendUint32(b, uint32(val))
}

// ioctl makes an ioctl that takes an int.
func ioctl(f *os.File, req uintptr, v int) error {
	return control(f, func(fd uintptr) unix.Errno {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(v))
		return errno
	})
}

// ioctlPtr makes an ioctl that takes a pointer.
func ioctlPtr(f *os.File, req uintptr, arg unsafe.Pointer) error {
	return control(f, func(fd uintptr) unix.Errno {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(arg))
		return errno
	})
}

func control(f *os.File, call func(fd uintptr) unix.Errno) error {
	sc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno unix.Errno
	if err := sc.Control(func(fd uintptr) { errno = call(fd) }); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}
