// gg-setup imports the pieces of a SteelSeries GG Windows installer that
// gg-linux needs: the Electron client, device specifications, database
// migrations and images. Nothing proprietary is shipped with gg-linux; this
// tool pulls it from an installer you already have.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"ggl/client"
	"ggl/internal/extract"
	"ggl/internal/paths"
)

func main() {
	installer := flag.String("installer", "", "path to SteelSeriesGG<version>Setup.exe")
	firmware := flag.Bool("with-firmware", false, "also import device firmware images (needed for firmware updates)")
	keep := flag.Bool("keep-temp", false, "keep the temporary unpack directory")
	bootstrap := flag.Bool("bootstrap", false, "only reinstall the Linux bootstrap into the imported client (after updating gg-linux)")
	current := flag.String("current", "", "exit 0 if this installer is already imported in full, 1 if it needs importing")
	flag.Parse()
	if *current != "" {
		if !imported(*current) {
			os.Exit(1)
		}
		return
	}
	if *bootstrap {
		if err := InstallBootstrap(filepath.Join(paths.Client(), "app")); err != nil {
			fmt.Fprintln(os.Stderr, "gg-setup:", err)
			os.Exit(1)
		}
		return
	}
	if *installer == "" && flag.NArg() > 0 {
		*installer = flag.Arg(0)
	}
	if *installer == "" {
		fmt.Fprintln(os.Stderr, "usage: gg-setup [--with-firmware] SteelSeriesGG<version>Setup.exe")
		os.Exit(2)
	}
	if err := run(*installer, *firmware, *keep); err != nil {
		fmt.Fprintln(os.Stderr, "gg-setup:", err)
		os.Exit(1)
	}
}

// importVersion counts what gg-setup takes from an installer; it goes up
// when a release imports more, so that data imported by an older gg-setup
// is imported again.
const importVersion = 2

// imported reports whether the data directory holds everything this
// gg-setup imports from the installer.
func imported(installer string) bool {
	var m struct {
		Installer string `json:"installer"`
		Import    int    `json:"import"`
	}
	b, err := os.ReadFile(filepath.Join(paths.Data(), "import.json"))
	if err != nil || json.Unmarshal(b, &m) != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(paths.Client(), "app", "package.json"))
	return err == nil && m.Installer == filepath.Base(installer) && m.Import == importVersion
}

func run(installer string, withFirmware, keep bool) error {
	tmp, err := os.MkdirTemp("", "gg-setup-")
	if err != nil {
		return err
	}
	if !keep {
		defer os.RemoveAll(tmp)
	}

	step("Unpacking installer")
	if err := extract.UnpackInstaller(installer, tmp, withFirmware); err != nil {
		return err
	}

	step("Recovering device specification key from SteelSeriesEngine.exe")
	pass, err := extract.SpecPassphrase(filepath.Join(tmp, "apps", "engine", "SteelSeriesEngine.exe"))
	if err != nil {
		return err
	}

	step("Decrypting device specifications")
	n, err := extract.DecryptSpecDir(filepath.Join(tmp, "apps", "engine", "deviceSpecifications"), paths.DeviceSpecs(), pass)
	if err != nil {
		return err
	}
	fmt.Printf("  %d device specifications\n", n)

	step("Installing client and resources")
	copies := []struct{ src, dst string }{
		{"resources/app.asar", paths.ClientAsar()},
		{"apps/engine/configurationMigrations", paths.Migrations()},
		{"apps/engine/db", filepath.Join(paths.EngineRoot(), "db")},
		{"apps/engine/assets", filepath.Join(paths.EngineRoot(), "assets")},
		{"apps/engine/lisp", filepath.Join(paths.EngineRoot(), "lisp")},
		{"apps/engine/game-integration", filepath.Join(paths.EngineRoot(), "game-integration")},
		{"apps/engine/sounds", filepath.Join(paths.EngineRoot(), "sounds")},
		{"apps/engine/fonts", filepath.Join(paths.EngineRoot(), "fonts")},
		{"apps/engine/prism/db", filepath.Join(paths.EngineRoot(), "prism", "db")},
		{"db", filepath.Join(paths.Data(), "gg", "db")},
		{"db-ggez", filepath.Join(paths.Data(), "gg", "db-ggez")},
		{"localization", paths.Localization()},
		{"shared/assets", filepath.Join(paths.Assets(), "shared")},
		{"resources/overlays", filepath.Join(paths.Assets(), "overlays")},
		{"apps/moments/sounds", filepath.Join(paths.Assets(), "moments", "sounds")},
		{"apps/sonar/db-migrations", paths.SonarDBMig()},
		// ImageSync's preset GIFs: the client loads them from GG's
		// install folder, which the bootstrap puts in the data directory.
		{"apps/engine/engineApps/imagesync/presets", filepath.Join(paths.Data(), "SteelSeries", "GG", "apps", "engine", "engineApps", "imageSync", "presets")},
		// The NSIS "$_153_" bundle carries the loadout device images and
		// Sonar audio samples.
		{"$_153_/apps/engine/assets", filepath.Join(paths.EngineRoot(), "assets")},
		{"$_153_/apps/sonar", filepath.Join(paths.Assets(), "sonar")},
	}
	if withFirmware {
		copies = append(copies, struct{ src, dst string }{"apps/engine/firmware", paths.Firmware()})
	}
	for _, c := range copies {
		src := filepath.Join(tmp, filepath.FromSlash(c.src))
		info, err := os.Stat(src)
		if err != nil {
			fmt.Printf("  skipping %s (not in this installer)\n", c.src)
			continue
		}
		if info.IsDir() {
			err = extract.CopyTree(src, c.dst)
		} else {
			err = extract.CopyFile(src, c.dst)
		}
		if err != nil {
			return fmt.Errorf("copy %s: %w", c.src, err)
		}
	}

	step("Extracting tray icons from SteelSeriesGG.exe")
	if n, err := extract.TrayIcons(filepath.Join(tmp, "SteelSeriesGG.exe"), paths.TrayIcons()); err != nil {
		fmt.Printf("  skipping tray icons: %v\n", err)
	} else {
		fmt.Printf("  %d icons\n", n)
	}

	step("Unpacking client")
	if err := installClient(); err != nil {
		return err
	}

	version := "unknown"
	if m := regexp.MustCompile(`(\d+\.\d+\.\d+)`).FindString(filepath.Base(installer)); m != "" {
		version = m
	}
	manifest := map[string]any{
		"ggVersion":  version,
		"import":     importVersion,
		"installer":  filepath.Base(installer),
		"importedAt": time.Now().UTC().Format(time.RFC3339),
		"firmware":   withFirmware,
		"specs":      n,
	}
	b, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(paths.Data(), "import.json"), b, 0o644); err != nil {
		return err
	}
	step("Done. Data directory: " + paths.Data())
	return nil
}

// installClient unpacks app.asar into client/app and points package.json at
// the Linux bootstrap. The original package.json is kept as package.orig.json.
func installClient() error {
	appDir := filepath.Join(paths.Client(), "app")
	if err := os.RemoveAll(appDir); err != nil {
		return err
	}
	if err := extract.UnpackAsar(paths.ClientAsar(), appDir); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(appDir, "package.json"), filepath.Join(appDir, "package.orig.json")); err != nil {
		return err
	}
	if err := InstallBootstrap(appDir); err != nil {
		return err
	}
	pkg := map[string]any{
		"name":        "steelseries-gg-client",
		"productName": "SteelSeries GG",
		"version":     "3.0.0",
		"main":        "gg-linux-entry.js",
		"private":     true,
	}
	b, _ := json.MarshalIndent(pkg, "", "  ")
	if err := os.WriteFile(filepath.Join(appDir, "package.json"), b, 0o644); err != nil {
		return err
	}
	// GG_LINUX_BOOTSTRAP lets a source checkout run its own bootstrap
	// without re-running gg-setup.
	entry := "require(process.env.GG_LINUX_BOOTSTRAP || require('path').join(__dirname, 'linux', 'bootstrap.js'));\n"
	return os.WriteFile(filepath.Join(appDir, "gg-linux-entry.js"), []byte(entry), 0o644)
}

// InstallBootstrap copies the embedded Linux bootstrap into appDir/linux.
func InstallBootstrap(appDir string) error {
	dst := filepath.Join(appDir, "linux")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := client.Linux.ReadDir("linux")
	if err != nil {
		return err
	}
	for _, e := range entries {
		b, err := client.Linux.ReadFile("linux/" + e.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func step(s string) { fmt.Println("==>", s) }
