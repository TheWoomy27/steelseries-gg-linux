package wineprefix

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func mkPrefix(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "drive_c", "ProgramData"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "system.reg"), []byte("WINE REGISTRY Version 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeHome lays out prefixes as Steam, Wine, Heroic and umu make them.
func fakeHome(t *testing.T) (home, otherLib string) {
	home = t.TempDir()
	otherLib = t.TempDir()
	steam := filepath.Join(home, ".local", "share", "Steam")
	os.MkdirAll(filepath.Join(steam, "steamapps"), 0o755)
	os.MkdirAll(filepath.Join(home, ".steam"), 0o755)
	os.Symlink(steam, filepath.Join(home, ".steam", "steam"))
	vdf := `"libraryfolders"
{
	"0"
	{
		"path"		"` + steam + `"
	}
	"1"
	{
		"path"		"` + otherLib + `"
	}
}`
	os.WriteFile(filepath.Join(steam, "steamapps", "libraryfolders.vdf"), []byte(vdf), 0o644)
	mkPrefix(t, filepath.Join(steam, "steamapps", "compatdata", "105600", "pfx"))
	os.MkdirAll(filepath.Join(steam, "steamapps", "compatdata", "1451480"), 0o755) // never run
	mkPrefix(t, filepath.Join(otherLib, "steamapps", "compatdata", "739630", "pfx"))
	mkPrefix(t, filepath.Join(home, ".wine"))
	heroic := filepath.Join(home, "Games", "Heroic", "Prefixes", "default")
	mkPrefix(t, heroic)
	os.Symlink(heroic, filepath.Join(heroic, "pfx"))
	mkPrefix(t, filepath.Join(heroic, "Rocket League"))
	mkPrefix(t, filepath.Join(home, "Games", "umu", "umu-default"))
	// A game folder is not a prefix.
	os.MkdirAll(filepath.Join(home, "Games", "Nintendo", "roms"), 0o755)
	return home, otherLib
}

func TestPrefixes(t *testing.T) {
	home, other := fakeHome(t)
	got := Prefixes(home)
	sort.Strings(got)
	want := []string{
		filepath.Join(home, ".local/share/Steam/steamapps/compatdata/105600/pfx"),
		filepath.Join(home, ".wine"),
		filepath.Join(home, "Games/Heroic/Prefixes/default"),
		filepath.Join(home, "Games/Heroic/Prefixes/default/Rocket League"),
		filepath.Join(home, "Games/umu/umu-default"),
		filepath.Join(other, "steamapps/compatdata/739630/pfx"),
	}
	for i := range want {
		want[i], _ = filepath.EvalSymlinks(want[i])
	}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("prefixes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestWrite(t *testing.T) {
	pfx := filepath.Join(t.TempDir(), "pfx")
	mkPrefix(t, pfx)
	props := []byte(`{"address":"127.0.0.1:1234"}`)
	if wrote, err := Write(pfx, props); err != nil || !wrote {
		t.Fatalf("first write: %v %v", wrote, err)
	}
	b, err := os.ReadFile(filepath.Join(pfx, "drive_c", "ProgramData", "SteelSeries", "SteelSeries Engine 3", "coreProps.json"))
	if err != nil || string(b) != string(props) {
		t.Fatalf("file: %q %v", b, err)
	}
	if wrote, _ := Write(pfx, props); wrote {
		t.Error("an unchanged file was written again")
	}
	if wrote, _ := Write(pfx, []byte(`{"address":"127.0.0.1:99"}`)); !wrote {
		t.Error("a new address was not written")
	}
}

func TestKeeperNewSteamPrefix(t *testing.T) {
	home, _ := fakeHome(t)
	props := []byte(`{"address":"127.0.0.1:4321"}`)
	k := &Keeper{Home: home, Props: props, Rescan: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { k.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	has := func(pfx string) bool {
		b, err := os.ReadFile(propsPath(pfx))
		return err == nil && string(b) == string(props)
	}
	wait := func(pfx string) bool {
		for i := 0; i < 40; i++ {
			if has(pfx) {
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	}
	if !wait(filepath.Join(home, ".wine")) || !wait(filepath.Join(home, "Games", "umu", "umu-default")) {
		t.Fatal("the prefixes there at the start were not written")
	}
	// Proton makes a prefix for a game run for the first time: the
	// compatdata folder first, the prefix a moment later.
	entry := filepath.Join(home, ".local", "share", "Steam", "steamapps", "compatdata", "2357570")
	os.MkdirAll(entry, 0o755)
	time.Sleep(700 * time.Millisecond)
	if has(filepath.Join(entry, "pfx")) {
		t.Fatal("written before the prefix was made")
	}
	mkPrefix(t, filepath.Join(entry, "pfx"))
	if !wait(filepath.Join(entry, "pfx")) {
		t.Fatal("the new prefix was not written")
	}
}
