// Package wlcopy captures a Wayland output's picture through the
// wlr-screencopy protocol (Hyprland, sway and other wlroots-style
// compositors): the compositor copies each frame into shared memory.
// It speaks just enough of the Wayland wire protocol for that.
package wlcopy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Pixel formats (wl_shm) a frame can come in.
const (
	FormatARGB8888 = 0
	FormatXRGB8888 = 1
	FormatABGR8888 = 0x34324241
	FormatXBGR8888 = 0x34324258
)

// Output is a monitor.
type Output struct {
	Name      string
	Width     int // mode, before the transform
	Height    int
	Transform int // wl_output transform (0 normal, 1 90°, 2 180°, 3 270°, 4-7 flipped)
	id        uint32
}

// Frame is a captured picture: Pix holds Height rows of Stride bytes,
// valid until the next capture.
type Frame struct {
	Width, Height, Stride int
	Format                uint32
	YInvert               bool
	Transform             int
	Pix                   []byte
}

// Client is a connection to the compositor.
type Client struct {
	mu      sync.Mutex
	conn    *net.UnixConn
	next    uint32
	free    []uint32
	rbuf    []byte
	fds     []int
	shm     uint32
	manager uint32
	outputs []*Output

	// The shared memory frames are copied into.
	pool    uint32
	buffer  uint32
	mem     []byte
	bufW    int
	bufH    int
	bufS    int
	bufFmt  uint32
	mgrVers uint32
}

const displayID = 1

// Open connects to the compositor and finds the screencopy protocol.
func Open() (*Client, error) {
	name := os.Getenv("WAYLAND_DISPLAY")
	if name == "" {
		return nil, errors.New("no Wayland display")
	}
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), name)
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, next: 2}
	type global struct {
		name, version uint32
		iface         string
	}
	var globals []global
	registry := c.newID()
	handlers := map[uint32]func(op uint16, a *args) error{
		registry: func(op uint16, a *args) error {
			if op == 0 {
				globals = append(globals, global{a.u32(), 0, a.str()})
				globals[len(globals)-1].version = a.u32()
			}
			return nil
		},
	}
	if err := c.send(displayID, 1, newArgs().put32(registry), nil); err != nil {
		conn.Close()
		return nil, err
	}
	if err := c.roundtrip(handlers); err != nil {
		conn.Close()
		return nil, err
	}
	bind := func(g global, version uint32) (uint32, error) {
		id := c.newID()
		return id, c.send(registry, 0, newArgs().put32(g.name).putStr(g.iface).put32(version).put32(id), nil)
	}
	for _, g := range globals {
		var err error
		switch g.iface {
		case "wl_shm":
			c.shm, err = bind(g, 1)
		case "zwlr_screencopy_manager_v1":
			c.mgrVers = min(g.version, 3)
			c.manager, err = bind(g, c.mgrVers)
		case "wl_output":
			o := &Output{}
			o.id, err = bind(g, min(g.version, 4))
			c.outputs = append(c.outputs, o)
			out := o
			handlers[o.id] = func(op uint16, a *args) error {
				switch op {
				case 0: // geometry
					for i := 0; i < 5; i++ {
						a.u32()
					}
					a.str()
					a.str()
					out.Transform = int(a.u32())
				case 1: // mode
					if a.u32()&1 != 0 { // current
						out.Width, out.Height = int(a.u32()), int(a.u32())
					}
				case 4: // name
					out.Name = a.str()
				}
				return nil
			}
		}
		if err != nil {
			conn.Close()
			return nil, err
		}
	}
	if c.manager == 0 || c.shm == 0 {
		conn.Close()
		return nil, errors.New("the compositor has no wlr-screencopy")
	}
	if err := c.roundtrip(handlers); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// Outputs lists the monitors.
func (c *Client) Outputs() []Output {
	out := make([]Output, len(c.outputs))
	for i, o := range c.outputs {
		out[i] = *o
	}
	return out
}

// CaptureTimeout is how long Capture waits for the compositor.
const CaptureTimeout = time.Second

// ErrUnchanged is CaptureChanged's answer when the monitor did not change
// within CaptureTimeout (or is off).
var ErrUnchanged = errors.New("wlcopy: the monitor did not change")

// Capture copies a monitor's current picture.
func (c *Client) Capture(output string) (*Frame, error) {
	return c.capture(output, false)
}

// CaptureChanged copies a monitor's picture the next time it changes.
// The compositor draws nothing extra for it, so it costs little while
// the screen is still.
func (c *Client) CaptureChanged(output string) (*Frame, error) {
	return c.capture(output, c.mgrVers >= 2)
}

func (c *Client) capture(output string, damage bool) (*Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out *Output
	for _, o := range c.outputs {
		if o.Name == output {
			out = o
		}
	}
	if out == nil {
		return nil, fmt.Errorf("no output %s", output)
	}
	frame := c.newID()
	if err := c.send(c.manager, 0, newArgs().put32(frame).put32(0).put32(out.id), nil); err != nil {
		return nil, err
	}
	f := &Frame{Transform: out.Transform}
	var haveBuffer, bufferDone, copied, ready, failed bool
	handlers := map[uint32]func(op uint16, a *args) error{
		frame: func(op uint16, a *args) error {
			switch op {
			case 0: // buffer (shm)
				f.Format, f.Width, f.Height, f.Stride = a.u32(), int(a.u32()), int(a.u32()), int(a.u32())
				haveBuffer = true
			case 1: // flags
				f.YInvert = a.u32()&1 != 0
			case 2:
				ready = true
			case 3:
				failed = true
			case 6:
				bufferDone = true
			}
			return nil
		},
	}
	defer c.send(frame, 1, nil, nil) // destroy
	// A monitor that is off may never be drawn, and so never copied.
	// Late events for the frame are dropped: its id is not reused before
	// the compositor deletes it.
	c.conn.SetReadDeadline(time.Now().Add(CaptureTimeout))
	defer c.conn.SetReadDeadline(time.Time{})
	for !ready && !failed {
		if err := c.dispatch(handlers); err != nil {
			if damage && errors.Is(err, os.ErrDeadlineExceeded) {
				return nil, ErrUnchanged
			}
			return nil, err
		}
		if haveBuffer && !copied && (bufferDone || c.mgrVers < 3) {
			if err := c.ensureBuffer(f); err != nil {
				return nil, err
			}
			op := uint16(0) // copy
			if damage {
				op = 2 // copy_with_damage
			}
			if err := c.send(frame, op, newArgs().put32(c.buffer), nil); err != nil {
				return nil, err
			}
			copied = true
		}
	}
	if failed {
		return nil, errors.New("the compositor could not copy the frame")
	}
	f.Pix = c.mem[:f.Stride*f.Height]
	return f, nil
}

// ensureBuffer makes the shared memory buffer for a frame's size.
func (c *Client) ensureBuffer(f *Frame) error {
	if c.buffer != 0 && c.bufW == f.Width && c.bufH == f.Height && c.bufS == f.Stride && c.bufFmt == f.Format {
		return nil
	}
	c.releaseBuffer()
	size := f.Stride * f.Height
	fd, err := unix.MemfdCreate("ggd-screencopy", unix.MFD_CLOEXEC)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		return err
	}
	mem, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return err
	}
	c.pool = c.newID()
	if err := c.send(c.shm, 0, newArgs().put32(c.pool).put32(uint32(size)), []int{fd}); err != nil {
		unix.Munmap(mem)
		return err
	}
	c.buffer = c.newID()
	if err := c.send(c.pool, 0, newArgs().put32(c.buffer).put32(0).put32(uint32(f.Width)).put32(uint32(f.Height)).put32(uint32(f.Stride)).put32(f.Format), nil); err != nil {
		unix.Munmap(mem)
		return err
	}
	c.mem, c.bufW, c.bufH, c.bufS, c.bufFmt = mem, f.Width, f.Height, f.Stride, f.Format
	return nil
}

func (c *Client) releaseBuffer() {
	if c.buffer != 0 {
		c.send(c.buffer, 0, nil, nil) // wl_buffer.destroy
		c.send(c.pool, 1, nil, nil)   // wl_shm_pool.destroy
		c.buffer, c.pool = 0, 0
	}
	if c.mem != nil {
		unix.Munmap(c.mem)
		c.mem = nil
	}
}

// Close disconnects.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releaseBuffer()
	c.conn.Close()
}

func (c *Client) newID() uint32 {
	if n := len(c.free); n > 0 {
		id := c.free[n-1]
		c.free = c.free[:n-1]
		return id
	}
	id := c.next
	c.next++
	return id
}

// send writes a request.
func (c *Client) send(obj uint32, op uint16, a *args, fds []int) error {
	var body []byte
	if a != nil {
		body = a.b
	}
	msg := make([]byte, 8+len(body))
	binary.LittleEndian.PutUint32(msg[0:], obj)
	binary.LittleEndian.PutUint32(msg[4:], uint32(len(msg))<<16|uint32(op))
	copy(msg[8:], body)
	var oob []byte
	if len(fds) > 0 {
		oob = unix.UnixRights(fds...)
	}
	_, _, err := c.conn.WriteMsgUnix(msg, oob, nil)
	return err
}

// roundtrip dispatches events until the compositor has handled every
// request sent so far.
func (c *Client) roundtrip(handlers map[uint32]func(uint16, *args) error) error {
	cb := c.newID()
	done := false
	handlers[cb] = func(uint16, *args) error { done = true; return nil }
	defer delete(handlers, cb)
	if err := c.send(displayID, 0, newArgs().put32(cb), nil); err != nil {
		return err
	}
	for !done {
		if err := c.dispatch(handlers); err != nil {
			return err
		}
	}
	return nil
}

// dispatch reads and handles the events available (at least one).
func (c *Client) dispatch(handlers map[uint32]func(uint16, *args) error) error {
	buf := make([]byte, 4096)
	oob := make([]byte, unix.CmsgSpace(4*8))
	for {
		if len(c.rbuf) >= 8 {
			size := int(binary.LittleEndian.Uint32(c.rbuf[4:]) >> 16)
			if size >= 8 && len(c.rbuf) >= size {
				obj := binary.LittleEndian.Uint32(c.rbuf[0:])
				op := uint16(binary.LittleEndian.Uint32(c.rbuf[4:]))
				a := &args{b: append([]byte(nil), c.rbuf[8:size]...)}
				c.rbuf = c.rbuf[size:]
				if obj == displayID {
					switch op {
					case 0: // error
						obj, code := a.u32(), a.u32()
						return fmt.Errorf("wayland error on object %d (code %d): %s", obj, code, a.str())
					case 1: // delete_id
						c.free = append(c.free, a.u32())
					}
				} else if h := handlers[obj]; h != nil {
					if err := h(op, a); err != nil {
						return err
					}
				}
				return nil
			}
		}
		n, oobn, _, _, err := c.conn.ReadMsgUnix(buf, oob)
		if err != nil {
			return err
		}
		if oobn > 0 {
			// The compositor sends no files this client wants; close any.
			if msgs, err := unix.ParseSocketControlMessage(oob[:oobn]); err == nil {
				for _, m := range msgs {
					if fds, err := unix.ParseUnixRights(&m); err == nil {
						for _, fd := range fds {
							unix.Close(fd)
						}
					}
				}
			}
		}
		if n == 0 {
			return errors.New("the compositor closed the connection")
		}
		c.rbuf = append(c.rbuf, buf[:n]...)
	}
}

// args reads or writes a message's arguments.
type args struct{ b []byte }

func newArgs() *args { return &args{} }

// put32 and putStr write arguments; u32 and str read them.
func (a *args) put32(v uint32) *args {
	a.b = binary.LittleEndian.AppendUint32(a.b, v)
	return a
}

func (a *args) putStr(s string) *args {
	a.b = binary.LittleEndian.AppendUint32(a.b, uint32(len(s)+1))
	a.b = append(a.b, s...)
	a.b = append(a.b, 0)
	for len(a.b)%4 != 0 {
		a.b = append(a.b, 0)
	}
	return a
}

func (a *args) u32() uint32 {
	if len(a.b) < 4 {
		return 0
	}
	x := binary.LittleEndian.Uint32(a.b)
	a.b = a.b[4:]
	return x
}

func (a *args) str() string {
	n := int(a.u32())
	if n == 0 || n > len(a.b) {
		return ""
	}
	s := string(a.b[:n-1])
	a.b = a.b[min((n+3)&^3, len(a.b)):]
	return s
}
