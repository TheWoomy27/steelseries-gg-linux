package prism

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"ggl/internal/wlcopy"
)

// TestReflectLiveCapture writes each monitor as Reflect sees it (180
// pixels high) to GG_LINUX_REFLECT_LIVE, a folder, to look at.
func TestReflectLiveCapture(t *testing.T) {
	dir := os.Getenv("GG_LINUX_REFLECT_LIVE")
	if dir == "" {
		t.Skip("GG_LINUX_REFLECT_LIVE not set")
	}
	c, err := wlcopy.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	t.Logf("focused: %s", focusedOutput(c))
	for _, o := range c.Outputs() {
		f, err := c.Capture(o.Name)
		if err != nil {
			t.Fatal(err)
		}
		s := downscale(f)
		img := image.NewRGBA(image.Rect(0, 0, s.w, s.h))
		for y := 0; y < s.h; y++ {
			for x := 0; x < s.w; x++ {
				p := s.pix[(y*s.w+x)*3:]
				img.Set(x, y, color.RGBA{uint8(p[0] * 255), uint8(p[1] * 255), uint8(p[2] * 255), 255})
			}
		}
		w, _ := os.Create(filepath.Join(dir, o.Name+".png"))
		png.Encode(w, img)
		w.Close()
		for _, a := range []int{areaLeft, areaRight, areaTop, areaBottom, areaAll} {
			t.Logf("%s area %d: %v -> %v", o.Name, a, s.average(areaRect(a)), saturate(s.average(areaRect(a))))
		}
	}
}

// TestReflectLiveCost measures the CPU one capture and scaling take.
func TestReflectLiveCost(t *testing.T) {
	if os.Getenv("GG_LINUX_REFLECT_LIVE") == "" {
		t.Skip("GG_LINUX_REFLECT_LIVE not set")
	}
	c, err := wlcopy.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	out := focusedOutput(c)
	cpu := func() time.Duration {
		var ru syscall.Rusage
		syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
		return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
	}
	const n = 40
	var capCPU, scaleCPU time.Duration
	start := time.Now()
	for i := 0; i < n; i++ {
		c0 := cpu()
		f, err := c.Capture(out)
		if err != nil {
			t.Fatal(err)
		}
		c1 := cpu()
		downscale(f)
		capCPU += c1 - c0
		scaleCPU += cpu() - c1
	}
	t.Logf("%s: %v per frame, CPU %v capturing and %v scaling", out, time.Since(start)/n, capCPU/n, scaleCPU/n)
}
