package extract

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
)

// trayIconGroups are the GROUP_ICON resources in SteelSeriesGG.exe that the
// Windows core shows in the notification area, by the state they stand for.
var trayIconGroups = map[string]string{
	"TRAYICON_OK":        "ok",
	"TRAYICON_RECORDING": "recording",
}

// TrayIcons writes GG's tray icons from SteelSeriesGG.exe into dst as
// <state>-<size>.png (ok-16.png ... recording-256.png) and returns how many
// it wrote.
func TrayIcons(exe, dst string) (int, error) {
	sevenZip, err := findSevenZip()
	if err != nil {
		return 0, err
	}
	tmp, err := os.MkdirTemp("", "gg-icons-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)
	// 7-Zip lists PE resources as .rsrc/<type>/<name>; RT_ICON entries come
	// out as single-image .ico files, or bare PNG data for PNG icons.
	cmd := exec.Command(sevenZip, "x", "-y", "-bso0", "-bsp0", "-o"+tmp, exe, ".rsrc/ICON/*", ".rsrc/GROUP_ICON/*")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("7z: %w", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, err
	}
	n := 0
	for group, state := range trayIconGroups {
		dir, err := os.ReadFile(filepath.Join(tmp, ".rsrc", "GROUP_ICON", group))
		if err != nil {
			return n, fmt.Errorf("%s: %w", group, err)
		}
		ids, err := groupIconIDs(dir)
		if err != nil {
			return n, fmt.Errorf("%s: %w", group, err)
		}
		for _, id := range ids {
			img, err := readIconResource(filepath.Join(tmp, ".rsrc", "ICON", fmt.Sprint(id)))
			if err != nil {
				return n, fmt.Errorf("%s icon %d: %w", group, id, err)
			}
			if err := writePNG(filepath.Join(dst, fmt.Sprintf("%s-%d.png", state, img.Bounds().Dx())), img); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// groupIconIDs reads the RT_ICON ids listed in a GRPICONDIR.
func groupIconIDs(b []byte) ([]uint16, error) {
	if len(b) < 6 {
		return nil, fmt.Errorf("short icon group")
	}
	count := int(binary.LittleEndian.Uint16(b[4:]))
	if len(b) < 6+14*count {
		return nil, fmt.Errorf("short icon group")
	}
	ids := make([]uint16, count)
	for i := range ids {
		ids[i] = binary.LittleEndian.Uint16(b[6+14*i+12:])
	}
	return ids, nil
}

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

func readIconResource(base string) (image.Image, error) {
	b, err := os.ReadFile(base + ".ico")
	if os.IsNotExist(err) {
		b, err = os.ReadFile(base)
	}
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(b, pngMagic) {
		// A single-image ICONDIR wrapping the resource data.
		if len(b) < 22 {
			return nil, fmt.Errorf("short .ico")
		}
		size := binary.LittleEndian.Uint32(b[14:])
		off := binary.LittleEndian.Uint32(b[18:])
		if uint64(off)+uint64(size) > uint64(len(b)) {
			return nil, fmt.Errorf("bad .ico entry")
		}
		b = b[off : off+size]
	}
	if bytes.HasPrefix(b, pngMagic) {
		return png.Decode(bytes.NewReader(b))
	}
	return decodeIconDIB(b)
}

// decodeIconDIB decodes a 32-bit icon bitmap: a BITMAPINFOHEADER whose
// height covers the colour and mask rows, then bottom-up BGRA rows. The
// alpha channel makes the trailing AND mask redundant.
func decodeIconDIB(b []byte) (image.Image, error) {
	if len(b) < 40 {
		return nil, fmt.Errorf("short icon bitmap")
	}
	w := int(int32(binary.LittleEndian.Uint32(b[4:])))
	h := int(int32(binary.LittleEndian.Uint32(b[8:]))) / 2
	bpp := binary.LittleEndian.Uint16(b[14:])
	hdr := int(binary.LittleEndian.Uint32(b[0:]))
	if bpp != 32 {
		return nil, fmt.Errorf("unsupported %d-bit icon", bpp)
	}
	if w <= 0 || h <= 0 || len(b) < hdr+w*h*4 {
		return nil, fmt.Errorf("bad icon bitmap size")
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	px := b[hdr:]
	for y := 0; y < h; y++ {
		row := px[(h-1-y)*w*4:]
		for x := 0; x < w; x++ {
			p := row[x*4:]
			img.SetNRGBA(x, y, color.NRGBA{R: p[2], G: p[1], B: p[0], A: p[3]})
		}
	}
	return img, nil
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
