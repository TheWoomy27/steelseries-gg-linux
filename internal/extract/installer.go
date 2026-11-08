package extract

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Windows installer layout (NSIS, unpacked with 7-Zip). Paths are relative
// to the installer root. Entries ending in "/" are directories.
var installerItems = []string{
	"resources/app.asar",
	"SteelSeriesGG.exe",
	"apps/engine/SteelSeriesEngine.exe",
	"apps/engine/deviceSpecifications/",
	"apps/engine/configurationMigrations/",
	"apps/engine/db/",
	"apps/engine/assets/",
	"apps/engine/lisp/",
	"apps/engine/game-integration/",
	"apps/engine/sounds/",
	"apps/engine/fonts/",
	"apps/engine/prism/db/",
	"db/",
	"db-ggez/",
	"localization/",
	"shared/assets/",
	"resources/overlays/",
	"apps/moments/sounds/",
	"apps/sonar/db-migrations/",
	"$_153_/",
}

// UnpackInstaller extracts the parts of SteelSeriesGG*Setup.exe that
// gg-linux uses into dir. It shells out to 7z, which understands NSIS.
func UnpackInstaller(installer, dir string, withFirmware bool) error {
	sevenZip, err := findSevenZip()
	if err != nil {
		return err
	}
	items := append([]string{}, installerItems...)
	if withFirmware {
		items = append(items, "apps/engine/firmware/")
	}
	args := []string{"x", "-y", "-bso0", "-bsp0", "-o" + dir, installer}
	for _, it := range items {
		if strings.HasSuffix(it, "/") {
			args = append(args, "-ir!"+strings.TrimSuffix(it, "/")+"/*")
		} else {
			args = append(args, "-i!"+it)
		}
	}
	cmd := exec.Command(sevenZip, args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("7z: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "resources", "app.asar")); err != nil {
		return fmt.Errorf("installer did not contain resources/app.asar: is this a SteelSeries GG installer?")
	}
	return nil
}

func findSevenZip() (string, error) {
	for _, name := range []string{"7z", "7zz", "7za"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("7-Zip is required to unpack the installer (install p7zip or 7zip)")
}

// CopyTree copies src into dst, creating directories as needed.
func CopyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return CopyFile(p, target)
	})
}

func CopyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
