package ggcore

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"ggl/internal/kwinfocus"
	"ggl/internal/paths"
)

// procRoot is /proc; tests point it at a made-up one.
var procRoot = "/proc"

// fileID names a directory however a path reaches it: Fedora Atomic's
// /home is a link to /var/home, and inside a distrobox the two are
// separate mounts of the same folder, so path strings do not compare.
type fileID struct{ dev, ino uint64 }

func idOf(path string) (fileID, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileID{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, false
	}
	return fileID{uint64(st.Dev), uint64(st.Ino)}, true
}

// gameIndex finds the library games processes belong to: Steam games by
// the SteamAppId environment variable, others by install folder, from the
// paths in the process's command line (a Wine process's is the Windows
// path of its .exe, looked up in its prefix) and its executable.
type gameIndex struct {
	games []ScannedGame
	byApp map[string]ScannedGame
	byDir map[fileID]ScannedGame
}

func newGameIndex(games []ScannedGame) *gameIndex {
	ix := &gameIndex{games: games, byApp: map[string]ScannedGame{}, byDir: map[fileID]ScannedGame{}}
	home := home()
	for _, g := range games {
		if g.steamAppID != "" {
			ix.byApp[g.steamAppID] = g
		}
		// A folder this high up would hold everything.
		d := filepath.Clean(g.Directory)
		if g.Directory == "" || strings.Count(d, "/") < 3 || d == filepath.Clean(home) {
			continue
		}
		if id, ok := idOf(d); ok {
			ix.byDir[id] = g
		}
	}
	return ix
}

func readProc(pid, name string) []byte {
	b, _ := os.ReadFile(filepath.Join(procRoot, pid, name))
	return b
}

func envOf(environ []byte, key string) string {
	for _, kv := range bytes.Split(environ, []byte{0}) {
		if v, ok := bytes.CutPrefix(kv, []byte(key+"=")); ok {
			return string(v)
		}
	}
	return ""
}

// procGames are the games process pid belongs to.
func (ix *gameIndex) procGames(pid string) []ScannedGame {
	found := map[string]ScannedGame{}
	env := readProc(pid, "environ")
	if g, ok := ix.byApp[envOf(env, "SteamAppId")]; ok {
		found[g.ID] = g
	}
	cmd := readProc(pid, "cmdline")
	if len(cmd) > 0 {
		line := string(bytes.ReplaceAll(cmd, []byte{0}, []byte{' '}))
		for _, g := range ix.games {
			if len(g.Directory) > 8 && strings.Contains(line, g.Directory) {
				found[g.ID] = g
			}
		}
		for _, p := range procPaths(cmd, env) {
			if g, ok := ix.underGame(p); ok {
				found[g.ID] = g
			}
		}
	}
	if exe, err := os.Readlink(filepath.Join(procRoot, pid, "exe")); err == nil {
		if g, ok := ix.underGame(exe); ok {
			found[g.ID] = g
		}
	}
	out := make([]ScannedGame, 0, len(found))
	for _, g := range found {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UniqueIdentifier < out[j].UniqueIdentifier })
	return out
}

// procPaths are the file paths in a command line: absolute ones as they
// are, and Windows ones (Wine names its processes by the .exe's Windows
// path, "C:\Program Files (x86)\World of Warcraft\_retail_\Wow.exe") in
// the process's Wine prefix.
func procPaths(cmdline, environ []byte) []string {
	var out []string
	for _, a := range strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00") {
		switch {
		case strings.HasPrefix(a, "/"):
			out = append(out, a)
		case isWindowsPath(a):
			prefix := envOf(environ, "WINEPREFIX")
			if prefix == "" {
				if h := envOf(environ, "HOME"); h != "" {
					prefix = filepath.Join(h, ".wine")
				}
			}
			if prefix != "" {
				out = append(out, foldPath(filepath.Join(prefix, "dosdevices", strings.ToLower(a[:2]), strings.ReplaceAll(a[3:], `\`, "/"))))
			}
		}
	}
	return out
}

func isWindowsPath(a string) bool {
	return len(a) > 3 && a[1] == ':' && (a[2] == '\\' || a[2] == '/') &&
		(a[0] >= 'A' && a[0] <= 'Z' || a[0] >= 'a' && a[0] <= 'z')
}

// foldPath finds path's components ignoring case, as Windows does, where
// they are not there as spelled.
func foldPath(path string) string {
	if _, err := os.Lstat(path); err == nil {
		return path
	}
	dir, name := filepath.Split(filepath.Clean(path))
	if dir == "/" || dir == "" {
		return path
	}
	dir = foldPath(filepath.Clean(dir))
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			if strings.EqualFold(e.Name(), name) {
				return filepath.Join(dir, e.Name())
			}
		}
	}
	return filepath.Join(dir, name)
}

// underGame finds the game whose install folder holds path, the nearest
// when folders nest.
func (ix *gameIndex) underGame(path string) (ScannedGame, bool) {
	if len(ix.byDir) == 0 {
		return ScannedGame{}, false
	}
	for p := filepath.Clean(path); p != "/" && p != "."; p = filepath.Dir(p) {
		if id, ok := idOf(p); ok {
			if g, ok := ix.byDir[id]; ok {
				return g, true
			}
		}
	}
	return ScannedGame{}, false
}

func pids() []string {
	ents, _ := os.ReadDir(procRoot)
	var out []string
	for _, e := range ents {
		if n := e.Name(); n[0] >= '0' && n[0] <= '9' {
			out = append(out, n)
		}
	}
	return out
}

// running are the library games with a process.
func (ix *gameIndex) running() []ScannedGame {
	found := map[string]ScannedGame{}
	for _, pid := range pids() {
		for _, g := range ix.procGames(pid) {
			found[g.ID] = g
		}
	}
	out := make([]ScannedGame, 0, len(found))
	for _, g := range found {
		out = append(out, g)
	}
	return out
}

// runningGames are the library games with a process.
func runningGames(games []ScannedGame) []ScannedGame { return newGameIndex(games).running() }

// windowGame finds the game a window belongs to.
func (ix *gameIndex) windowGame(w kwinfocus.Window) (ScannedGame, bool) {
	// Proton classes its X11 windows by app id.
	if id, ok := strings.CutPrefix(w.Class, "steam_app_"); ok {
		if g, ok := ix.byApp[id]; ok {
			return g, true
		}
	}
	if w.PID <= 0 {
		return ScannedGame{}, false
	}
	pid := strconv.Itoa(w.PID)
	if gs := ix.procGames(pid); len(gs) > 0 {
		return gs[0], true
	}
	// An X11 window can carry the pid its client has in a pid namespace
	// of its own (Flatpak launchers such as Faugus, and so umu and
	// pressure-vessel under them): look for processes with that pid
	// there, preferring the one whose executable the class names
	// (Wine classes windows "wow.exe").
	var best []ScannedGame
	for _, p := range pids() {
		if p == pid || nsPID(p) != pid {
			continue
		}
		gs := ix.procGames(p)
		if len(gs) == 0 {
			continue
		}
		if best == nil || classMatches(w.Class, p) {
			best = gs
		}
	}
	if len(best) > 0 {
		return best[0], true
	}
	return ScannedGame{}, false
}

// nsPID is a process's pid in its innermost pid namespace, "" when that
// is the root namespace (or unknown).
func nsPID(pid string) string {
	for _, l := range strings.Split(string(readProc(pid, "status")), "\n") {
		if f, ok := strings.CutPrefix(l, "NSpid:"); ok {
			if ids := strings.Fields(f); len(ids) > 1 {
				return ids[len(ids)-1]
			}
			return ""
		}
	}
	return ""
}

// classMatches says whether a window class names process pid: Wine
// classes windows by the program ("wow.exe" for ...\_retail_\Wow.exe),
// Proton by game id ("steam_app_<SteamGameId>").
func classMatches(class, pid string) bool {
	if class == "" {
		return false
	}
	arg0, _, _ := strings.Cut(string(readProc(pid, "cmdline")), "\x00")
	arg0 = arg0[strings.LastIndexAny(arg0, `/\`)+1:]
	if strings.EqualFold(arg0, class) {
		return true
	}
	id := envOf(readProc(pid, "environ"), "SteamGameId")
	return id != "" && class == "steam_app_"+id
}

// isGGWindow says whether a window is GG's own: GG's client runs from
// its data folder.
func isGGWindow(w kwinfocus.Window) bool {
	if w.PID <= 0 {
		return false
	}
	return bytes.Contains(readProc(strconv.Itoa(w.PID), "cmdline"), []byte(paths.Client()))
}
