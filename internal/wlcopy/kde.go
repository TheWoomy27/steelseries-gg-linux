package wlcopy

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// KWin has no wlr-screencopy. It streams monitors through PipeWire
// instead, to clients that ask with its zkde_screencast_unstable_v1
// protocol (Spectacle and xdg-desktop-portal-kde use it; KWin 6 offers it
// to any client that is not sandboxed, so there is no permission dialog).
// The stream asked for is a region of the desktop, the monitor's, at a
// scale that makes it StreamHeight pixels high: KWin draws it that small
// on the GPU, so little is copied. GStreamer's pipewiresrc reads it (Go
// has no PipeWire video client) and hands raw RGBx frames over a pipe.
// KWin sends a frame only when the monitor changes, and at most
// MaxRate a second as the stream's format asks.

// StreamHeight is the height KDE streams are scaled to: what Reflect
// samples.
const StreamHeight = 180

// MaxRate is the most frames a second a KDE stream sends.
const MaxRate = 10

// zkde_screencast_unstable_v1 requests and pointer modes.
const (
	kdeStreamOutput = 0
	kdeStreamRegion = 4 // since version 3
	kdePointerHide  = 1
)

// kdeState is the stream of a monitor.
type kdeState struct {
	output string // the monitor streamed; "" when none
	stream uint32
	w, h   int // the frames' size
	gst    *exec.Cmd
	frames chan []byte // the latest frame
	done   chan error  // the reader's end
	closed bool        // KWin stopped the stream
	got    bool        // a frame came since the stream started
	last   []byte
}

// regionArgs is stream_region's message: the monitor's logical rectangle
// at the scale that makes it height pixels high.
func regionArgs(stream uint32, o *Output, height int) *args {
	scale := int32(math.Round(float64(height) / float64(o.logH) * 256)) // wl_fixed
	return newArgs().put32(stream).put32(uint32(int32(o.logX))).put32(uint32(int32(o.logY))).
		put32(uint32(o.logW)).put32(uint32(o.logH)).put32(uint32(max(scale, 1))).put32(kdePointerHide)
}

// outputArgs is stream_output's message: the monitor at full size.
func outputArgs(stream uint32, o *Output) *args {
	return newArgs().put32(stream).put32(o.id).put32(kdePointerHide)
}

// streamSize is the size frames are scaled to: height pixels high, the
// monitor's shape (its logical size, else its mode as shown).
func streamSize(o *Output, height int) (w, h int) {
	ow, oh := o.logW, o.logH
	if ow <= 0 || oh <= 0 {
		ow, oh = o.Width, o.Height
		if o.Transform%2 == 1 {
			ow, oh = oh, ow
		}
	}
	if ow <= 0 || oh <= 0 {
		return height * 16 / 9, height
	}
	return max(int(math.Round(float64(ow)*float64(height)/float64(oh))), 1), height
}

// gstArgs is the gst-launch-1.0 command line reading a stream from
// source (pipewiresrc and its target). The frames come out on stdout as
// w x h RGBx.
func gstArgs(source []string, w, h int) []string {
	a := append([]string{"-q"}, source...)
	return append(a,
		// System memory (no DMA-BUF), and KWin's rate limit. (KWin delays
		// a frame that comes too soon instead of dropping it; GStreamer's
		// videorate would drop it, and with it the screen's last change.)
		// (Caps are capsfilter elements: gst-launch takes no bare caps
		// right after a source's own caps.)
		"!", "capsfilter", fmt.Sprintf(`caps="video/x-raw,max-framerate=%d/1"`, MaxRate),
		// KWin scaled a region stream already; an output stream is scaled here.
		"!", "videoscale", "add-borders=false",
		"!", "videoconvert",
		"!", "capsfilter", fmt.Sprintf(`caps="video/x-raw,format=RGBx,width=%d,height=%d,pixel-aspect-ratio=1/1"`, w, h),
		"!", "fdsink", "fd=1", "sync=false")
}

// newSource is the GStreamer source for a KWin stream (tests replace it).
var newSource = pipewireSource

// pipewireSource is the pipewiresrc element for a KWin stream.
func pipewireSource(serial uint64, haveSerial bool, node uint32) []string {
	if haveSerial {
		return []string{"pipewiresrc", "target-object=" + strconv.FormatUint(serial, 10)}
	}
	return []string{"pipewiresrc", "path=" + strconv.FormatUint(uint64(node), 10)}
}

var gstChecked struct {
	sync.Mutex
	at  time.Time
	err error
}

// gstCheck reports whether GStreamer can read PipeWire streams. A missing
// piece is looked for again after a minute, not at each capture attempt.
func gstCheck() error {
	gstChecked.Lock()
	defer gstChecked.Unlock()
	if gstChecked.err != nil && time.Since(gstChecked.at) < time.Minute {
		return gstChecked.err
	}
	gstChecked.at, gstChecked.err = time.Now(), lookForGst()
	return gstChecked.err
}

func lookForGst() error {
	if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
		return errors.New("KDE screen capture needs gst-launch-1.0 (GStreamer tools: gstreamer1 on Fedora, gstreamer1.0-tools on Debian/Ubuntu, gstreamer on Arch)")
	}
	if exec.Command("gst-inspect-1.0", "--exists", "pipewiresrc").Run() != nil {
		return errors.New("KDE screen capture needs GStreamer's PipeWire plugin (pipewire-gstreamer on Fedora, gstreamer1.0-pipewire on Debian/Ubuntu, gst-plugin-pipewire on Arch)")
	}
	return nil
}

// kdeCapture is Capture and CaptureChanged on KWin: the next frame of the
// monitor's stream (any frame since the stream started, unless changed).
func (c *Client) kdeCapture(output string, changed bool) (*Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := &c.kde
	if k.output != output || k.closed {
		c.kdeStop()
		if err := c.kdeStart(output); err != nil {
			c.kdeStop()
			return nil, err
		}
	}
	if !changed && k.got {
		return k.frame(k.last), nil
	}
	if err := c.pollEvents(); err != nil {
		return nil, err
	}
	if k.closed {
		return nil, errors.New("KWin stopped the screen stream")
	}
	timeout := CaptureTimeout
	if !k.got {
		timeout += 2 * time.Second // GStreamer starting
	}
	select {
	case pix := <-k.frames:
		k.got, k.last = true, pix
		return k.frame(pix), nil
	case err := <-k.done:
		k.closed = true
		return nil, err
	case <-time.After(timeout):
		if changed {
			return nil, ErrUnchanged
		}
		return nil, errors.New("no frame from the screen stream")
	}
}

func (k *kdeState) frame(pix []byte) *Frame {
	return &Frame{Width: k.w, Height: k.h, Stride: k.w * 4, Format: FormatXBGR8888, Pix: pix}
}

// pollEvents handles the events that came, without waiting.
func (c *Client) pollEvents() error {
	c.conn.SetReadDeadline(time.Now())
	defer c.conn.SetReadDeadline(time.Time{})
	for {
		if err := c.dispatch(c.kdeHandlers()); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return nil
			}
			return err
		}
	}
}

func (c *Client) kdeHandlers() map[uint32]func(uint16, *args) error {
	h := map[uint32]func(uint16, *args) error{}
	if c.kde.stream != 0 {
		h[c.kde.stream] = func(op uint16, a *args) error {
			if op == 0 { // closed
				c.kde.closed = true
			}
			return nil
		}
	}
	return h
}

// kdeStart asks KWin for a monitor's stream and starts reading it.
func (c *Client) kdeStart(output string) error {
	var out *Output
	for _, o := range c.outputs {
		if o.Name == output {
			out = o
		}
	}
	if out == nil {
		return fmt.Errorf("no output %s", output)
	}
	k := &c.kde
	*k = kdeState{output: output, stream: c.newID()}
	var a *args
	op := uint16(kdeStreamOutput)
	if c.mgrVers >= 3 && out.logW > 0 && out.logH > 0 {
		op, a = kdeStreamRegion, regionArgs(k.stream, out, StreamHeight)
	} else {
		a = outputArgs(k.stream, out)
	}
	k.w, k.h = streamSize(out, StreamHeight)
	if err := c.send(c.manager, op, a, nil); err != nil {
		return err
	}
	var serial uint64
	var node uint32
	var haveSerial, created bool
	var failed string
	handlers := c.kdeHandlers()
	handlers[k.stream] = func(op uint16, a *args) error {
		switch op {
		case 0: // closed
			k.closed = true
		case 1: // created
			node, created = a.u32(), true
		case 2: // failed
			failed = a.str()
		case 3: // serial
			hi, lo := a.u32(), a.u32()
			serial, haveSerial = uint64(hi)<<32|uint64(lo), true
		}
		return nil
	}
	c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer c.conn.SetReadDeadline(time.Time{})
	for !created && failed == "" && !k.closed {
		if err := c.dispatch(handlers); err != nil {
			return fmt.Errorf("KWin did not start the screen stream: %w", err)
		}
	}
	switch {
	case failed != "":
		return fmt.Errorf("KWin could not stream %s: %s", output, failed)
	case k.closed:
		return fmt.Errorf("KWin closed the stream of %s", output)
	}
	cmd := exec.Command("gst-launch-1.0", gstArgs(newSource(serial, haveSerial, node), k.w, k.h)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	stderr := &tailWriter{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	k.gst = cmd
	k.frames, k.done = make(chan []byte, 1), make(chan error, 1)
	go readFrames(stdout, k.w*k.h*4, k.frames, k.done, func() error {
		cmd.Wait()
		return fmt.Errorf("gst-launch-1.0 ended: %s", stderr.String())
	})
	return nil
}

// readFrames reads frames of size bytes from r, keeping only the latest
// in frames, until r ends; then end's error goes to done.
func readFrames(r io.Reader, size int, frames chan []byte, done chan error, end func() error) {
	for {
		pix := make([]byte, size)
		if _, err := io.ReadFull(r, pix); err != nil {
			done <- end()
			return
		}
		select {
		case <-frames:
		default:
		}
		frames <- pix
	}
}

// kdeStop ends the stream.
func (c *Client) kdeStop() {
	k := &c.kde
	if k.gst != nil {
		k.gst.Process.Kill()
		k.gst = nil
	}
	if k.stream != 0 {
		c.send(k.stream, 0, nil, nil) // close
	}
	*k = kdeState{}
}

// tailWriter keeps the end of a program's messages.
type tailWriter struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 2048 {
		t.b = t.b[len(t.b)-2048:]
	}
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := strings.TrimSpace(string(t.b))
	if s == "" {
		return "no message"
	}
	return s
}
