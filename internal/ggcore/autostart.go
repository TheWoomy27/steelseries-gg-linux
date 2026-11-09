package ggcore

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// GG's Auto-run setting ("Run SteelSeries GG when my computer starts") is
// the systemd user service packaging/install.sh installs, which starts
// ggd with the graphical session. Without the service the setting is only
// stored.

const autostartUnit = "steelseries-ggd.service"

func userUnitDir() string {
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		home, _ := os.UserHomeDir()
		config = filepath.Join(home, ".config")
	}
	return filepath.Join(config, "systemd", "user")
}

// autostartInstalled reports whether the service is installed, and if so
// whether it starts with the session.
func autostartInstalled() (installed, enabled bool) {
	dir := userUnitDir()
	if _, err := os.Stat(filepath.Join(dir, autostartUnit)); err != nil {
		return false, false
	}
	_, err := os.Lstat(filepath.Join(dir, "graphical-session.target.wants", autostartUnit))
	return true, err == nil
}

// setAutostart enables or disables the service; a running ggd keeps
// running either way.
func setAutostart(on bool) {
	if installed, enabled := autostartInstalled(); !installed || enabled == on {
		return
	}
	verb := "disable"
	if on {
		verb = "enable"
	}
	if out, err := exec.Command("systemctl", "--user", verb, autostartUnit).CombinedOutput(); err != nil {
		slog.Warn("auto-run", "verb", verb, "err", err, "out", string(out))
	}
}
