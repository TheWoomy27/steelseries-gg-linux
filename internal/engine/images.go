package engine

import (
	_ "embed"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"ggl/internal/paths"
)

// Device pictures for the home page, QuickSet's tiles and device
// onboarding: the Engine's loadoutImages, imported by gg-setup and served
// at /assets/loadoutImages as on Windows. Windows picks a picture with a
// hand-written table of device names (device_images.tsv), by the variant
// the device is set to, else its own name. gg-linux does the same; for a
// name the table doesn't have it looks for a picture of that name, then
// the model's (the longest picture name the device's name starts with:
// aerox_5 for aerox_5_wireless), then a generic one for its type.

//go:embed device_images.tsv
var windowsImagesTSV string

var windowsImages = func() map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(windowsImagesTSV, "\n") {
		name, pic, ok := strings.Cut(line, "\t")
		if ok && !strings.HasPrefix(line, "#") {
			m[name] = strings.TrimSpace(pic)
		}
	}
	return m
}()

func loadoutImagesDir() string { return filepath.Join(paths.EngineRoot(), "assets", "loadoutImages") }

var (
	imagesOnce sync.Once
	imageNames []string // device pictures without .webp, longest first
)

func deviceImageNames() []string {
	imagesOnce.Do(func() {
		entries, _ := os.ReadDir(filepath.Join(loadoutImagesDir(), "devices"))
		for _, e := range entries {
			if n, ok := strings.CutSuffix(e.Name(), ".webp"); ok && !e.IsDir() {
				imageNames = append(imageNames, n)
			}
		}
		sort.Slice(imageNames, func(i, j int) bool { return len(imageNames[i]) > len(imageNames[j]) })
	})
	return imageNames
}

// deviceImage is the picture for a device, by its variant's and its own
// name, as a path under loadoutImages; "" if there is none.
func deviceImage(names []string, deviceType int) string {
	pictures := deviceImageNames()
	for _, n := range names {
		if p, ok := windowsImages[n]; ok && slices.Contains(pictures, p) {
			return "devices/" + p + ".webp"
		}
	}
	for _, n := range names {
		for _, p := range pictures {
			if n == p {
				return "devices/" + p + ".webp"
			}
		}
	}
	for _, n := range names {
		for _, p := range pictures {
			if strings.HasPrefix(n, p+"_") {
				return "devices/" + p + ".webp"
			}
		}
	}
	generic := map[string]string{"keyboard": "keyboard", "mouse": "mouse", "headset": "headset", "controller": "controller", "mousepad": "mousepad"}[typeName(deviceType)]
	if generic == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Join(loadoutImagesDir(), "devices", "generics", generic+".webp")); err != nil {
		return ""
	}
	return "devices/generics/" + generic + ".webp"
}

// imageURL is where the client loads a device picture from.
func (e *Engine) imageURL(image string) string {
	if image == "" {
		return ""
	}
	e.mu.Lock()
	addr := e.addr
	e.mu.Unlock()
	return "https://" + addr + "/assets/loadoutImages/" + image
}

func (e *Engine) serveImages(w http.ResponseWriter, r *http.Request) {
	rel := filepath.Clean("/" + r.PathValue("path"))
	if filepath.Ext(rel) != ".webp" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "max-age=86400")
	http.ServeFile(w, r, filepath.Join(loadoutImagesDir(), rel))
}
