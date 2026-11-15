package wlcopy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPickBackend(t *testing.T) {
	hypr := []string{"wl_shm", "zwlr_screencopy_manager_v1", "wl_output"}
	kwin := []string{"wl_shm", "zkde_screencast_unstable_v1", "wl_output"}
	both := append(slices.Clone(hypr), "zkde_screencast_unstable_v1")
	gnome := []string{"wl_shm", "wl_output"}
	for _, tc := range []struct {
		globals []string
		want    string
		backend Backend
		err     bool
	}{
		{hypr, "", BackendScreencopy, false},
		{kwin, "", BackendKDE, false},
		{kwin, "auto", BackendKDE, false},
		{both, "", BackendScreencopy, false},
		{both, "kde", BackendKDE, false},
		{kwin, "screencopy", 0, true},
		{hypr, "kde", 0, true},
		{gnome, "", 0, true},
		{both, "portal", 0, true},
		{[]string{"zwlr_screencopy_manager_v1"}, "", 0, true}, // no wl_shm
	} {
		b, err := pickBackend(func(i string) bool { return slices.Contains(tc.globals, i) }, tc.want)
		if b != tc.backend || (err != nil) != tc.err {
			t.Errorf("%v want %q: %v, %v; want %v (error %v)", tc.globals, tc.want, b, err, tc.backend, tc.err)
		}
	}
}

func words(a *args) []uint32 {
	var w []uint32
	for b := a.b; len(b) >= 4; b = b[4:] {
		w = append(w, binary.LittleEndian.Uint32(b))
	}
	return w
}

func TestStreamMessages(t *testing.T) {
	right := &Output{Name: "DP-1", Width: 2560, Height: 1440, id: 7, logX: 2560, logW: 2560, logH: 1440}
	// 180/1440 = 0.125 = 32/256 in wl_fixed.
	if got, want := words(regionArgs(9, right, 180)), []uint32{9, 2560, 0, 2560, 1440, 32, kdePointerHide}; !slices.Equal(got, want) {
		t.Errorf("stream_region %v, want %v", got, want)
	}
	// A 4K monitor at 150%: logical 2560x1440 too.
	left := &Output{Width: 3840, Height: 2160, logX: -2560, logY: 0, logW: 2560, logH: 1440}
	if got := words(regionArgs(9, left, 180)); int32(got[1]) != -2560 || got[5] != 32 {
		t.Errorf("negative position: %v", got)
	}
	tall := &Output{logW: 100, logH: 100000}
	if got := words(regionArgs(9, tall, 180)); got[5] != 1 {
		t.Errorf("tiny scale %d, want at least 1/256", got[5])
	}
	if got, want := words(outputArgs(9, right)), []uint32{9, 7, kdePointerHide}; !slices.Equal(got, want) {
		t.Errorf("stream_output %v, want %v", got, want)
	}
}

func TestStreamSize(t *testing.T) {
	for _, tc := range []struct {
		o    Output
		w, h int
	}{
		{Output{Width: 2560, Height: 1440, logW: 2560, logH: 1440}, 320, 180},
		{Output{Width: 3440, Height: 1440}, 430, 180},
		{Output{Width: 2560, Height: 1440, Transform: 1}, 101, 180}, // portrait
		{Output{Width: 2560, Height: 1440, Transform: 3, logW: 1440, logH: 2560}, 101, 180},
		{Output{}, 320, 180},
	} {
		if w, h := streamSize(&tc.o, 180); w != tc.w || h != tc.h {
			t.Errorf("%+v: %dx%d, want %dx%d", tc.o, w, h, tc.w, tc.h)
		}
	}
}

func TestGstArgs(t *testing.T) {
	got := strings.Join(gstArgs(pipewireSource(42, true, 7), 320, 180), " ")
	want := `-q pipewiresrc target-object=42 ! capsfilter caps="video/x-raw,max-framerate=10/1" ! ` +
		`videoscale add-borders=false ! videoconvert ! ` +
		`capsfilter caps="video/x-raw,format=RGBx,width=320,height=180,pixel-aspect-ratio=1/1" ! fdsink fd=1 sync=false`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := pipewireSource(0, false, 77); !slices.Equal(got, []string{"pipewiresrc", "path=77"}) {
		t.Errorf("before version 6: %v", got)
	}
}

func TestReadFrames(t *testing.T) {
	frames, done := make(chan []byte, 1), make(chan error, 1)
	// Three frames and a half: only the latest is kept, the half is dropped.
	data := []byte{1, 1, 2, 2, 3, 3, 4}
	readFrames(bytes.NewReader(data), 2, frames, done, func() error { return io.EOF })
	if f := <-frames; !bytes.Equal(f, []byte{3, 3}) {
		t.Errorf("latest frame %v", f)
	}
	if err := <-done; err != io.EOF {
		t.Errorf("end %v", err)
	}
}

// fakeKWin answers stream requests on the compositor's end of conn as
// KWin 6 does: serial then created, or failed. It passes on each request.
func fakeKWin(conn net.Conn, fail string) chan []uint32 {
	reqs := make(chan []uint32, 16)
	go func() {
		defer close(reqs)
		hdr := make([]byte, 8)
		for {
			if _, err := io.ReadFull(conn, hdr); err != nil {
				return
			}
			obj, size := binary.LittleEndian.Uint32(hdr), int(binary.LittleEndian.Uint32(hdr[4:])>>16)
			op := uint32(uint16(binary.LittleEndian.Uint32(hdr[4:])))
			body := make([]byte, size-8)
			io.ReadFull(conn, body)
			w := []uint32{obj, op}
			for b := body; len(b) >= 4; b = b[4:] {
				w = append(w, binary.LittleEndian.Uint32(b))
			}
			reqs <- w
			if obj != 3 { // not the manager
				continue
			}
			stream := w[2]
			if fail != "" {
				conn.Write(event(stream, 2, newArgs().putStr(fail).b))
				continue
			}
			conn.Write(event(stream, 3, newArgs().put32(0).put32(4242).b))
			conn.Write(event(stream, 1, newArgs().put32(77).b))
		}
	}()
	return reqs
}

func event(obj uint32, op uint16, body []byte) []byte {
	m := binary.LittleEndian.AppendUint32(nil, obj)
	m = binary.LittleEndian.AppendUint32(m, uint32(8+len(body))<<16|uint32(op))
	return append(m, body...)
}

// fakeClient is a client of KWin (manager object 3, one monitor DP-1)
// and KWin's end of its connection.
func fakeClient(t *testing.T) (*Client, net.Conn) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := net.FileConn(os.NewFile(uintptr(fds[0]), "client"))
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := net.FileConn(os.NewFile(uintptr(fds[1]), "kwin"))
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{conn: mine.(*net.UnixConn), next: 10, backend: BackendKDE, manager: 3, mgrVers: 6,
		outputs: []*Output{{Name: "DP-1", Width: 2560, Height: 1440, id: 5, logX: 2560, logW: 2560, logH: 1440}}}
	t.Cleanup(func() { theirs.Close() })
	return c, theirs
}

// TestKDECapture runs a stream end to end against a fake KWin, with
// GStreamer's test picture (solid red, at 2560x1440) for the PipeWire
// stream. It needs gst-launch-1.0 and GStreamer's base plugins.
func TestKDECapture(t *testing.T) {
	if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
		t.Skip("this test needs gst-launch-1.0 (GStreamer tools and base plugins)")
	}
	if exec.Command("gst-inspect-1.0", "--exists", "videotestsrc", "videoscale", "videoconvert", "fdsink").Run() != nil {
		t.Skip("this test needs GStreamer's base plugins (videotestsrc, videoscale, videoconvert)")
	}
	var gotSerial uint64
	newSource = func(serial uint64, haveSerial bool, node uint32) []string {
		if haveSerial {
			gotSerial = serial
		}
		return []string{"videotestsrc", "pattern=red", "num-buffers=3", "!", "video/x-raw,format=BGRx,width=2560,height=1440,framerate=30/1"}
	}
	defer func() { newSource = pipewireSource }()
	c, kwin := fakeClient(t)
	reqs := fakeKWin(kwin, "")
	// GStreamer can take more than the first wait to start on a busy
	// machine; ggd asks again then, and so does the test.
	f, err := c.CaptureChanged("DP-1")
	for i := 0; i < 4 && errors.Is(err, ErrUnchanged); i++ {
		f, err = c.CaptureChanged("DP-1")
	}
	if err != nil {
		t.Fatal(err)
	}
	if gotSerial != 4242 {
		t.Errorf("serial %d, want 4242", gotSerial)
	}
	if f.Width != 320 || f.Height != 180 || f.Stride != 1280 || f.Format != FormatXBGR8888 || len(f.Pix) != 320*180*4 {
		t.Fatalf("frame %dx%d stride %d format %#x, %d bytes", f.Width, f.Height, f.Stride, f.Format, len(f.Pix))
	}
	if p := f.Pix[(90*320+160)*4:]; p[0] != 255 || p[1] != 0 || p[2] != 0 {
		t.Errorf("middle pixel %v, want red", p[:4])
	}
	// The stream ends after its 3 frames: the client says so.
	for i := 0; i < 5; i++ {
		_, err = c.CaptureChanged("DP-1")
		if err != nil && !errors.Is(err, ErrUnchanged) {
			break
		}
	}
	if err == nil || errors.Is(err, ErrUnchanged) || !strings.Contains(err.Error(), "gst-launch-1.0 ended") {
		t.Errorf("after the stream ended: %v", err)
	}
	c.Close()
	r := <-reqs
	if want := []uint32{3, kdeStreamRegion, 10, 2560, 0, 2560, 1440, 32, kdePointerHide}; !slices.Equal(r, want) {
		t.Errorf("request %v, want %v", r, want)
	}
	if r := <-reqs; !slices.Equal(r, []uint32{10, 0}) {
		t.Errorf("then %v, want the stream's close", r)
	}
}

func TestKDECaptureFailed(t *testing.T) {
	c, kwin := fakeClient(t)
	fakeKWin(kwin, "not allowed")
	defer c.Close()
	start := time.Now()
	_, err := c.CaptureChanged("DP-1")
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("error %v", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("took %v", time.Since(start))
	}
	if _, err := c.CaptureChanged("HDMI-A-1"); err == nil || !strings.Contains(err.Error(), "no output") {
		t.Errorf("unknown output: %v", err)
	}
}
