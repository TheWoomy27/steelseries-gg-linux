package ggcore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAutostartInstalled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	units := filepath.Join(dir, "systemd", "user")
	check := func(what string, installed, enabled bool) {
		t.Helper()
		if i, e := autostartInstalled(); i != installed || e != enabled {
			t.Errorf("%s: installed %v enabled %v, want %v %v", what, i, e, installed, enabled)
		}
	}
	check("no service", false, false)
	os.MkdirAll(filepath.Join(units, "graphical-session.target.wants"), 0o755)
	os.WriteFile(filepath.Join(units, autostartUnit), []byte("[Unit]\n"), 0o644)
	check("installed", true, false)
	os.Symlink(filepath.Join(units, autostartUnit), filepath.Join(units, "graphical-session.target.wants", autostartUnit))
	check("enabled", true, true)
}
