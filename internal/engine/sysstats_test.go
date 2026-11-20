package engine

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"ggl/internal/oled"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	for name, v := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// An Intel CPU's package sensor, and an AMD card next to the processor's
// own graphics (less video memory), read through sysfs.
func TestSysStats(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", t.TempDir()) // no nvidia-smi
	old := sysRoot
	sysRoot = root
	defer func() { sysRoot = old }()
	writeFiles(t, root, map[string]string{
		"class/hwmon/hwmon0/name":                         "nvme",
		"class/hwmon/hwmon0/temp1_input":                  "38000",
		"class/hwmon/hwmon1/name":                         "coretemp",
		"class/hwmon/hwmon1/temp1_label":                  "Core 0",
		"class/hwmon/hwmon1/temp1_input":                  "40000",
		"class/hwmon/hwmon1/temp2_label":                  "Package id 0",
		"class/hwmon/hwmon1/temp2_input":                  "45500",
		"class/drm/card0/device/gpu_busy_percent":         "3",
		"class/drm/card0/device/mem_info_vram_total":      "536870912",
		"class/drm/card1/device/gpu_busy_percent":         "37",
		"class/drm/card1/device/mem_info_vram_total":      "17163091968",
		"class/drm/card1/device/hwmon/hwmon5/temp1_input": "61000",
	})
	if c, ok := cpuTemp(); !ok || c != 46 {
		t.Errorf("CPU temperature %d (%v), want the package's 46", c, ok)
	}
	var g gpuMeter
	if !g.available() {
		t.Fatal("no card found")
	}
	if load, temp, ok := g.read(); load != 37 || temp != 61 || !ok {
		t.Errorf("card %d%% %d°C (%v), want the discrete card's 37%% 61°C", load, temp, ok)
	}
	if got := statLine(37, 61, true); got != "37%  61°C" {
		t.Errorf("line %q", got)
	}

	// Three readings fit a 128x40 screen (GG_LINUX_PICTURES=dir to look).
	c := oled.Render(128, 40, nil, []oled.Line{{Text: "CPU 100%  100°C"}, {Text: "GPU 100%  100°C"}, {Text: "RAM 100%"}})
	if dir := os.Getenv("GG_LINUX_PICTURES"); dir != "" {
		f, _ := os.Create(filepath.Join(dir, "system-stats.png"))
		png.Encode(f, c.Image())
		f.Close()
	}
}
