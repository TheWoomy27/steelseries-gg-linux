package wlcopy

import (
	"os"
	"testing"
	"time"
)

// TestLiveCapture captures each monitor of the running compositor. It
// only runs with GG_LINUX_WLCOPY_LIVE set (it needs a Wayland session).
func TestLiveCapture(t *testing.T) {
	if os.Getenv("GG_LINUX_WLCOPY_LIVE") == "" {
		t.Skip("GG_LINUX_WLCOPY_LIVE not set")
	}
	c, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	outs := c.Outputs()
	if len(outs) == 0 {
		t.Fatal("no outputs")
	}
	for _, o := range outs {
		t.Logf("output %+v", o)
		start := time.Now()
		const n = 20
		var f *Frame
		for i := 0; i < n; i++ {
			if f, err = c.Capture(o.Name); err != nil {
				t.Fatal(err)
			}
		}
		per := time.Since(start) / n
		var sum [3]int
		samples := 0
		for y := 0; y < f.Height; y += 16 {
			for x := 0; x < f.Width; x += 16 {
				p := f.Pix[y*f.Stride+x*4:]
				sum[0] += int(p[0])
				sum[1] += int(p[1])
				sum[2] += int(p[2])
				samples++
			}
		}
		t.Logf("%s: %dx%d stride %d format %#x y-invert %v transform %d, %v per capture, mean bytes %d %d %d",
			o.Name, f.Width, f.Height, f.Stride, f.Format, f.YInvert, f.Transform, per, sum[0]/samples, sum[1]/samples, sum[2]/samples)
	}
}

// TestLiveCaptureChanged waits for a few changes of the focused monitor.
func TestLiveCaptureChanged(t *testing.T) {
	if os.Getenv("GG_LINUX_WLCOPY_LIVE") == "" {
		t.Skip("GG_LINUX_WLCOPY_LIVE not set")
	}
	c, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	out := os.Getenv("GG_LINUX_WLCOPY_OUTPUT")
	if out == "" {
		out = c.Outputs()[0].Name
	}
	for i := 0; i < 5; i++ {
		start := time.Now()
		_, err := c.CaptureChanged(out)
		t.Logf("%s: %v after %v", out, err, time.Since(start))
		if err != nil && err != ErrUnchanged {
			t.Fatal(err)
		}
	}
	if _, err := c.Capture(out); err != nil {
		t.Fatalf("a plain capture after: %v", err)
	}
}
