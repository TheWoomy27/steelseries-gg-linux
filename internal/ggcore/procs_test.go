package ggcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ggl/internal/kwinfocus"
)

// fakeProc makes a /proc entry.
func fakeProc(t *testing.T, root, pid string, cmdline []string, env []string, nspid string) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Wine pads its rewritten command line with NULs.
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("cmdline", strings.Join(cmdline, "\x00")+"\x00\x00\x00\x00")
	write("environ", strings.Join(env, "\x00")+"\x00")
	ns := pid
	if nspid != "" {
		ns += "\t" + nspid
	}
	write("status", "Name:\tx\nPid:\t"+pid+"\nNSpid:\t"+ns+"\nVmPeak:\t1 kB\n")
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// procWorld is a machine like the Fedora Atomic one QuickSet was fixed
// on: /home links to /var/home, World of Warcraft runs in a Faugus (umu,
// Proton) prefix in a Flatpak's pid namespace, Dota 2 under Steam.
type procWorld struct {
	games              []ScannedGame
	dota, wow, zygor   ScannedGame
	proc, prefix, home string
}

func newProcWorld(t *testing.T) procWorld {
	root := t.TempDir()
	t.Setenv("GG_LINUX_DATA", filepath.Join(root, "var/home/u/.local/share/steelseries-gg"))
	realHome := filepath.Join(root, "var/home/u")
	home := filepath.Join(root, "home/u")
	mkdirs(t, realHome)
	if err := os.Symlink("var/home", filepath.Join(root, "home")); err != nil {
		t.Fatal(err)
	}

	// Faugus's prefix, as umu sees it through its pfx link.
	prefix := filepath.Join(realHome, "Faugus/battlenet")
	mkdirs(t, filepath.Join(prefix, "drive_c/Program Files (x86)/World of Warcraft/_retail_"),
		filepath.Join(prefix, "drive_c/users/steamuser/AppData/Local/Zygor"), filepath.Join(prefix, "dosdevices"))
	os.Symlink(".", filepath.Join(prefix, "pfx"))
	os.Symlink("../drive_c", filepath.Join(prefix, "dosdevices/c:"))
	os.Symlink("/", filepath.Join(prefix, "dosdevices/z:"))
	dotaDir := filepath.Join(home, ".local/share/Steam/steamapps/common/dota 2 beta")
	mkdirs(t, dotaDir)

	w := procWorld{proc: filepath.Join(root, "proc"), prefix: prefix + "/pfx/", home: home}
	w.dota = ScannedGame{ID: "steam-570", Name: "Dota 2", Directory: dotaDir, UniqueIdentifier: "1_570", steamAppID: "570"}
	// Added by hand through GG's file picker, by the /home path.
	wowExe := filepath.Join(home, "Faugus/battlenet/drive_c/Program Files (x86)/World of Warcraft/_retail_/Wow.exe")
	w.wow = ScannedGame{ID: "manual-wow", Name: "Wow", ExePath: wowExe, Directory: filepath.Dir(wowExe), UniqueIdentifier: "manual-wow"}
	zyExe := filepath.Join(home, "Faugus/battlenet/drive_c/users/steamuser/AppData/Local/Zygor/Zygor.exe")
	w.zygor = ScannedGame{ID: "manual-zygor", Name: "Zygor", ExePath: zyExe, Directory: filepath.Dir(zyExe), UniqueIdentifier: "manual-zygor"}
	w.games = []ScannedGame{w.dota, w.wow, w.zygor}
	procRoot = w.proc
	t.Cleanup(func() { procRoot = "/proc" })
	return w
}

func names(gs []ScannedGame) string {
	var n []string
	for _, g := range gs {
		n = append(n, g.Name)
	}
	return strings.Join(n, ",")
}

func TestRunningGamesInWinePrefixes(t *testing.T) {
	w := newProcWorld(t)
	umuEnv := []string{"WINEPREFIX=" + w.prefix, "SteamAppId=faugus72815ab8", "SteamGameId=faugus72815ab8"}
	// Battle.net started WoW: its command line is the Windows path only.
	fakeProc(t, w.proc, "5000", []string{`C:\Program Files (x86)\World of Warcraft\_retail_\Wow.exe`, "-launcherlogin", "-uid", "wow_enus"}, umuEnv, "219")
	fakeProc(t, w.proc, "5001", []string{`C:\windows\system32\services.exe`}, umuEnv, "133")
	fakeProc(t, w.proc, "5002", []string{"/usr/bin/bash"}, nil, "")
	if got := names(runningGames(w.games)); got != "Wow" {
		t.Errorf("running %q, want Wow", got)
	}

	// Windows paths ignore case.
	fakeProc(t, w.proc, "5003", []string{`c:\USERS\steamuser\appdata\local\zygor\zygor.exe`, "--hidden"}, umuEnv, "250")
	fakeProc(t, w.proc, "6000", []string{"dota2"}, []string{"SteamAppId=570"}, "")
	ix := newGameIndex(w.games)
	if got := ix.procGames("5003"); names(got) != "Zygor" {
		t.Errorf("zygor.exe is %q", names(got))
	}
	if got := ix.procGames("6000"); names(got) != "Dota 2" {
		t.Errorf("dota2 is %q", names(got))
	}
}

func TestRunningGameByNativePath(t *testing.T) {
	w := newProcWorld(t)
	// A native game run by its /var/home path, the library has the /home one.
	exe := strings.Replace(w.dota.Directory, "/home/u/", "/var/home/u/", 1) + "/game/bin/linuxsteamrt64/dota2"
	fakeProc(t, w.proc, "100", []string{exe, "-novid"}, nil, "")
	if got := names(runningGames([]ScannedGame{{ID: "x", Name: "Dota", Directory: w.dota.Directory}})); got != "Dota" {
		t.Errorf("running %q, want Dota", got)
	}
}

func TestWindowGame(t *testing.T) {
	w := newProcWorld(t)
	umuEnv := []string{"WINEPREFIX=" + w.prefix, "SteamGameId=faugus72815ab8"}
	fakeProc(t, w.proc, "5000", []string{`C:\Program Files (x86)\World of Warcraft\_retail_\Wow.exe`}, umuEnv, "219")
	// Another sandbox's process with the same pid in its namespace.
	fakeProc(t, w.proc, "7000", []string{`C:\users\steamuser\AppData\Local\Zygor\Zygor.exe`}, append(umuEnv[:1:1], "SteamGameId=other"), "219")
	fakeProc(t, w.proc, "219", []string{"/usr/lib/systemd/systemd-journald"}, nil, "") // host pid 219
	fakeProc(t, w.proc, "6000", []string{"dota2"}, []string{"SteamAppId=570"}, "")
	fakeProc(t, w.proc, "300", []string{"/usr/lib64/firefox/firefox"}, nil, "")
	fakeProc(t, w.proc, "400", []string{"/usr/bin/electron", filepath.Join(os.Getenv("GG_LINUX_DATA"), "client", "app")}, nil, "")
	ix := newGameIndex(w.games)

	for _, c := range []struct {
		name string
		win  kwinfocus.Window
		want string
	}{
		{"host pid", kwinfocus.Window{PID: 5000, Class: "wow.exe"}, "Wow"},
		{"namespace pid, class names the exe", kwinfocus.Window{PID: 219, Class: "wow.exe"}, "Wow"},
		{"namespace pid, class names the exe 2", kwinfocus.Window{PID: 219, Class: "zygor.exe"}, "Zygor"},
		{"namespace pid, Proton class", kwinfocus.Window{PID: 219, Class: "steam_app_faugus72815ab8"}, "Wow"},
		{"Steam app class", kwinfocus.Window{PID: 99999, Class: "steam_app_570"}, "Dota 2"},
		{"Steam env", kwinfocus.Window{PID: 6000, Class: "dota2"}, "Dota 2"},
		{"not a game", kwinfocus.Window{PID: 300, Class: "firefox"}, ""},
		{"desktop", kwinfocus.Window{}, ""},
	} {
		g, ok := ix.windowGame(c.win)
		if got := map[bool]string{true: g.Name}[ok]; got != c.want {
			t.Errorf("%s: %+v is %q, want %q", c.name, c.win, got, c.want)
		}
	}

	running := ix.running()
	if !isGGWindow(kwinfocus.Window{PID: 400}) || isGGWindow(kwinfocus.Window{PID: 300}) {
		t.Error("GG's window not told apart")
	}
	// Following the window in front.
	front, keep := frontGame(kwinfocus.Window{PID: 300}, true, ix, running, "manual-wow")
	if front != nil || keep {
		t.Errorf("browser in front: %v %v, want Default", front, keep)
	}
	front, keep = frontGame(kwinfocus.Window{PID: 219, Class: "wow.exe"}, true, ix, running, "")
	if front == nil || front.Name != "Wow" || keep {
		t.Errorf("WoW in front: %v %v", front, keep)
	}
	if _, keep = frontGame(kwinfocus.Window{PID: 400}, true, ix, running, "manual-wow"); !keep {
		t.Error("GG in front changed the profile")
	}
	// Not knowing the window: a running game counts as in front.
	front, _ = frontGame(kwinfocus.Window{}, false, ix, running, "manual-wow")
	if front == nil || front.Name != "Wow" {
		t.Errorf("without the window: %v, want the playing game kept", front)
	}
}
