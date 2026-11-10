// Package wineprefix tells Windows games running under Wine or Proton
// where GameSense is. The GameSense SDK reads the Engine's address from
// %PROGRAMDATA%\SteelSeries\SteelSeries Engine 3\coreProps.json, which
// for a game in a Wine prefix is a file inside the prefix; ggd writes it
// into every prefix it finds, and into Steam's new ones as Proton makes
// them. Wine's sockets are the host's, so 127.0.0.1 reaches ggd.
package wineprefix

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// propsPath is where a prefix keeps the file.
func propsPath(prefix string) string {
	return filepath.Join(prefix, "drive_c", "ProgramData", "SteelSeries", "SteelSeries Engine 3", "coreProps.json")
}

// isPrefix reports whether dir is a made Wine prefix: its registry and
// C: drive are there.
func isPrefix(dir string) bool {
	if st, err := os.Stat(filepath.Join(dir, "system.reg")); err != nil || !st.Mode().IsRegular() {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, "drive_c"))
	return err == nil && st.IsDir()
}

// steamRoots are Steam installations: the native one (~/.steam/steam
// links to it) and Flatpak's.
func steamRoots(home string) []string {
	return []string{
		filepath.Join(home, ".local", "share", "Steam"),
		filepath.Join(home, ".steam", "steam"),
		filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam"),
	}
}

var vdfPath = regexp.MustCompile(`"path"\s+"((?:[^"\\]|\\.)*)"`)

// compatDirs are the steamapps/compatdata folders of every Steam
// library, from each installation's libraryfolders.vdf.
func compatDirs(home string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(lib string) {
		d := filepath.Join(lib, "steamapps", "compatdata")
		if r, err := filepath.EvalSymlinks(d); err == nil && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	for _, root := range steamRoots(home) {
		add(root)
		b, err := os.ReadFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
		if err != nil {
			continue
		}
		for _, m := range vdfPath.FindAllStringSubmatch(string(b), -1) {
			add(strings.ReplaceAll(m[1], `\\`, `\`))
		}
	}
	return out
}

// otherRoots are where other launchers keep prefixes, and how deep to
// look in them: Wine's own, Lutris and umu (~/Games/<game>), Heroic
// (~/Games/Heroic/Prefixes/default/<game>) and Bottles.
func otherRoots(home string) map[string]int {
	return map[string]int{
		filepath.Join(home, ".wine"):                                                               0,
		filepath.Join(home, "Games"):                                                               2,
		filepath.Join(home, "Games", "Heroic", "Prefixes"):                                         2,
		filepath.Join(home, ".local", "share", "bottles", "bottles"):                               1,
		filepath.Join(home, ".var", "app", "com.usebottles.bottles", "data", "bottles", "bottles"): 1,
	}
}

// findUnder lists the prefixes in dir and its folders, depth levels
// down, without looking inside a prefix.
func findUnder(dir string, depth int, out map[string]bool) {
	if isPrefix(dir) {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			out[r] = true
		}
	}
	if depth == 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != "drive_c" && e.Name() != "dosdevices" && !strings.HasPrefix(e.Name(), ".") {
			findUnder(filepath.Join(dir, e.Name()), depth-1, out)
		}
	}
}

// Prefixes finds the Wine prefixes in home: Steam's Proton prefixes in
// every library, and those of Wine, Lutris, umu, Heroic and Bottles.
func Prefixes(home string) []string {
	found := map[string]bool{}
	for _, c := range compatDirs(home) {
		entries, _ := os.ReadDir(c)
		for _, e := range entries {
			if e.IsDir() {
				findUnder(filepath.Join(c, e.Name(), "pfx"), 0, found)
			}
		}
	}
	for root, depth := range otherRoots(home) {
		findUnder(root, depth, found)
	}
	out := make([]string, 0, len(found))
	for p := range found {
		out = append(out, p)
	}
	return out
}

// Write puts the file in a prefix unless it already holds it, and
// reports whether it wrote.
func Write(prefix string, props []byte) (bool, error) {
	path := propsPath(prefix)
	if b, err := os.ReadFile(path); err == nil && bytes.Equal(b, props) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, props, 0o644); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

// Keeper keeps coreProps.json current in the prefixes.
type Keeper struct {
	Home  string
	Props []byte

	// Rescan is how often all prefixes are looked for again (new Steam
	// libraries, other launchers' prefixes, files a prefix reset lost).
	Rescan time.Duration

	mu      sync.Mutex
	pending map[string]time.Time // new Steam prefixes being made, by compatdata entry
}

// Run writes the file into every prefix, then watches Steam's libraries
// for prefixes Proton makes, until ctx ends.
func (k *Keeper) Run(ctx context.Context) {
	if k.Rescan == 0 {
		k.Rescan = 10 * time.Minute
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		slog.Warn("wine prefixes: no inotify", "err", err)
		fd = -1
	} else {
		defer unix.Close(fd)
	}
	watched := map[int32]string{}
	watchedDirs := map[string]bool{}
	scan := func() {
		n := k.writeAll()
		if fd < 0 {
			return
		}
		for _, c := range compatDirs(k.Home) {
			if watchedDirs[c] {
				continue
			}
			wd, err := unix.InotifyAddWatch(fd, c, unix.IN_CREATE|unix.IN_MOVED_TO)
			if err == nil {
				watched[int32(wd)] = c
				watchedDirs[c] = true
			}
		}
		if n > 0 {
			slog.Info("wine prefixes: told games where GameSense is", "prefixes", n)
		}
	}
	scan()
	rescan := time.NewTicker(k.Rescan)
	defer rescan.Stop()
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	buf := make([]byte, 64*1024)
	for {
		select {
		case <-ctx.Done():
			return
		case <-rescan.C:
			scan()
		case <-poll.C:
		}
		if fd >= 0 {
			for {
				n, err := unix.Read(fd, buf)
				if n <= 0 || err != nil {
					break
				}
				for off := 0; off+unix.SizeofInotifyEvent <= n; {
					ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
					name := ""
					if ev.Len > 0 {
						name = strings.TrimRight(string(buf[off+unix.SizeofInotifyEvent:off+unix.SizeofInotifyEvent+int(ev.Len)]), "\x00")
					}
					if dir, ok := watched[ev.Wd]; ok && name != "" {
						k.mu.Lock()
						if k.pending == nil {
							k.pending = map[string]time.Time{}
						}
						k.pending[filepath.Join(dir, name)] = time.Now()
						k.mu.Unlock()
					}
					off += unix.SizeofInotifyEvent + int(ev.Len)
				}
			}
		}
		k.checkPending()
	}
}

// checkPending writes into new Steam prefixes as soon as Proton has made
// them (it copies a ready prefix in, before starting the game).
func (k *Keeper) checkPending() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for entry, since := range k.pending {
		pfx := filepath.Join(entry, "pfx")
		if isPrefix(pfx) {
			if _, err := Write(pfx, k.Props); err != nil {
				slog.Warn("wine prefixes: write", "prefix", pfx, "err", err)
			} else {
				slog.Info("wine prefixes: told a new prefix where GameSense is", "prefix", pfx)
			}
			delete(k.pending, entry)
		} else if time.Since(since) > 10*time.Minute {
			delete(k.pending, entry)
		}
	}
}

// writeAll writes the file wherever it is missing or old, and reports
// how many prefixes it wrote.
func (k *Keeper) writeAll() int {
	n := 0
	for _, p := range Prefixes(k.Home) {
		wrote, err := Write(p, k.Props)
		if err != nil {
			slog.Warn("wine prefixes: write", "prefix", p, "err", err)
		} else if wrote {
			n++
		}
	}
	return n
}
