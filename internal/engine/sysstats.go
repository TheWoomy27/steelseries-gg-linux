package engine

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Readings for the System Stats screen app besides CPU and memory use,
// as Windows GG's System Monitor shows them: the CPU's temperature, and
// the graphics card's load and temperature.

// sysRoot is where the kernel's sysfs is (tests use a fake one).
var sysRoot = "/sys"

// cpuTemp is the CPU package's temperature in °C, from the CPU sensor
// driver's hwmon: Intel's coretemp (its package sensor), AMD's k10temp or
// zenpower (Tctl), or a board's cpu_thermal.
func cpuTemp() (int, bool) {
	prefer := map[string][]string{
		"coretemp":    {"Package id 0"},
		"k10temp":     {"Tctl", "Tdie"},
		"zenpower":    {"Tctl", "Tdie"},
		"cpu_thermal": nil,
	}
	hwmons, _ := filepath.Glob(filepath.Join(sysRoot, "class/hwmon/hwmon*"))
	for _, h := range hwmons {
		name := readTrim(filepath.Join(h, "name"))
		labels, ok := prefer[name]
		if !ok {
			continue
		}
		inputs, _ := filepath.Glob(filepath.Join(h, "temp*_input"))
		for _, want := range labels {
			for _, in := range inputs {
				if readTrim(strings.TrimSuffix(in, "_input")+"_label") == want {
					if t, ok := milliDegrees(in); ok {
						return t, true
					}
				}
			}
		}
		if t, ok := milliDegrees(filepath.Join(h, "temp1_input")); ok {
			return t, true
		}
	}
	return 0, false
}

// gpuMeter reads the graphics card's load and temperature: an NVIDIA card
// through nvidia-smi (at most every 5 seconds), else the AMD card with
// the most video memory (a discrete card over the processor's own) from
// the amdgpu driver's sysfs.
type gpuMeter struct {
	checked bool
	nvidia  string // nvidia-smi's path
	amd     string // the AMD card's device folder
	at      time.Time
	load    int
	temp    int
	hasTemp bool
}

// available reports whether there is a card to read, looking once.
func (g *gpuMeter) available() bool {
	if !g.checked {
		g.checked = true
		if p, err := exec.LookPath("nvidia-smi"); err == nil {
			if _, _, _, err := queryNvidia(p); err == nil {
				g.nvidia = p
			}
		}
		if g.nvidia == "" {
			best := uint64(0)
			cards, _ := filepath.Glob(filepath.Join(sysRoot, "class/drm/card[0-9]*/device"))
			for _, c := range cards {
				if _, err := os.Stat(filepath.Join(c, "gpu_busy_percent")); err != nil {
					continue
				}
				vram, _ := strconv.ParseUint(readTrim(filepath.Join(c, "mem_info_vram_total")), 10, 64)
				if g.amd == "" || vram > best {
					g.amd, best = c, vram
				}
			}
		}
	}
	return g.nvidia != "" || g.amd != ""
}

// read is the card's load in percent and its temperature in °C (ok false
// when it has no sensor).
func (g *gpuMeter) read() (load, temp int, hasTemp bool) {
	switch {
	case g.nvidia != "":
		if time.Since(g.at) >= 5*time.Second {
			g.at = time.Now()
			if l, t, ok, err := queryNvidia(g.nvidia); err == nil {
				g.load, g.temp, g.hasTemp = l, t, ok
			}
		}
	case g.amd != "":
		g.load, _ = strconv.Atoi(readTrim(filepath.Join(g.amd, "gpu_busy_percent")))
		g.hasTemp = false
		hwmons, _ := filepath.Glob(filepath.Join(g.amd, "hwmon/hwmon*"))
		for _, h := range hwmons {
			if t, ok := milliDegrees(filepath.Join(h, "temp1_input")); ok {
				g.temp, g.hasTemp = t, true
				break
			}
		}
	}
	return g.load, g.temp, g.hasTemp
}

func queryNvidia(path string) (load, temp int, hasTemp bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--query-gpu=utilization.gpu,temperature.gpu", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0, 0, false, err
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n") // the first card
	f := strings.Split(line, ",")
	if len(f) != 2 {
		return 0, 0, false, strconv.ErrSyntax
	}
	l, err := strconv.ParseFloat(strings.TrimSpace(f[0]), 64)
	if err != nil {
		return 0, 0, false, err
	}
	t, terr := strconv.ParseFloat(strings.TrimSpace(f[1]), 64)
	return int(math.Round(l)), int(math.Round(t)), terr == nil, nil
}

func readTrim(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

func milliDegrees(path string) (int, bool) {
	v, err := strconv.Atoi(readTrim(path))
	if err != nil {
		return 0, false
	}
	return int(math.Round(float64(v) / 1000)), true
}

// statLine is a reading for the screen: "12%", or "12%  45°C" with a
// temperature.
func statLine(percent, temp int, hasTemp bool) string {
	s := strconv.Itoa(percent) + "%"
	if hasTemp {
		s += "  " + strconv.Itoa(temp) + "°C"
	}
	return s
}
