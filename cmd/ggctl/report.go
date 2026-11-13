package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"ggl/internal/hid"
	"ggl/internal/paths"
)

// reportCommand writes what a bug report needs into one text file: the
// system, the SteelSeries devices and access to them, and the ends of the
// logs. The home folder, user name, host name and e-mail addresses are
// replaced, and device serial numbers are left out.
func reportCommand(args []string) error {
	out := fmt.Sprintf("gg-linux-report-%s.txt", time.Now().Format("20060102-150405"))
	if len(args) > 0 {
		out = args[0]
	}
	var b bytes.Buffer
	section := func(title string) { fmt.Fprintf(&b, "\n===== %s =====\n", title) }

	fmt.Fprintf(&b, "gg-linux report, %s\n", time.Now().Format(time.RFC3339))

	section("System")
	fmt.Fprintf(&b, "os: %s\n", osRelease())
	var uts unix.Utsname
	if unix.Uname(&uts) == nil {
		fmt.Fprintf(&b, "kernel: %s %s\n", unix.ByteSliceToString(uts.Release[:]), unix.ByteSliceToString(uts.Machine[:]))
	}
	fmt.Fprintf(&b, "desktop: %s (%s)\n", os.Getenv("XDG_CURRENT_DESKTOP"), os.Getenv("XDG_SESSION_TYPE"))
	fmt.Fprintf(&b, "gg-linux: %s\n", buildVersion())
	if imp, err := os.ReadFile(filepath.Join(paths.Data(), "import.json")); err == nil {
		fmt.Fprintf(&b, "imported GG: %s\n", bytes.Join(bytes.Fields(imp), []byte(" ")))
	} else {
		fmt.Fprintf(&b, "imported GG: none (%v)\n", err)
	}
	b.WriteString(command("ggd service: ", "systemctl", "--user", "is-active", "steelseries-ggd"))
	b.WriteString(command("electron: ", "sh", "-c", "for e in electron39 electron38 electron37 electron; do command -v $e >/dev/null && { echo \"$e $($e --version 2>/dev/null)\"; break; }; done"))

	section("Device access")
	if rules, err := os.ReadFile("/etc/udev/rules.d/70-steelseries-gg.rules"); err == nil {
		fmt.Fprintf(&b, "udev rule installed (%d lines):\n", strings.Count(string(rules), "\n"))
		for _, l := range strings.Split(string(rules), "\n") {
			if l != "" && !strings.HasPrefix(l, "#") {
				fmt.Fprintf(&b, "    %s\n", l)
			}
		}
	} else {
		fmt.Fprintf(&b, "udev rule: not installed\n")
	}
	fmt.Fprintf(&b, "/dev/uinput: %s\n", access("/dev/uinput"))

	section("SteelSeries USB devices")
	usb, _ := filepath.Glob("/sys/bus/usb/devices/*")
	sort.Strings(usb)
	for _, p := range usb {
		if strings.TrimSpace(readFile(p+"/idVendor")) != "1038" {
			continue
		}
		fmt.Fprintf(&b, "1038:%s  %s  (%s Mb/s)\n", strings.TrimSpace(readFile(p+"/idProduct")),
			strings.TrimSpace(readFile(p+"/product")), strings.TrimSpace(readFile(p+"/speed")))
	}

	section("hidraw nodes")
	if infos, err := hid.Enumerate(); err != nil {
		fmt.Fprintf(&b, "error: %v\n", err)
	} else {
		sort.Slice(infos, func(i, j int) bool { return infos[i].Path < infos[j].Path })
		for _, in := range infos {
			var cols []string
			for _, c := range in.Desc.Collections {
				cols = append(cols, fmt.Sprintf("%04x:%04x", c.UsagePage, c.Usage))
			}
			fmt.Fprintf(&b, "%-14s %04x:%04x if%d  %-10s %s  [%s]\n", in.Path, in.VendorID, in.ProductID, in.Interface,
				access(in.Path), in.Name, strings.Join(cols, " "))
		}
	}

	section("SteelSeries input devices")
	events, _ := filepath.Glob("/sys/class/input/event*")
	for _, p := range events {
		if strings.TrimSpace(readFile(p+"/device/id/vendor")) != "1038" {
			continue
		}
		node := "/dev/input/" + filepath.Base(p)
		fmt.Fprintf(&b, "%-18s 1038:%s  %-10s %s\n", node, strings.TrimSpace(readFile(p+"/device/id/product")),
			access(node), strings.TrimSpace(readFile(p+"/device/name")))
	}

	section("Audio (wpctl status)")
	b.WriteString(command("", "wpctl", "status"))

	logs := []struct {
		name  string
		lines int
	}{{"ggd.log", 4000}, {"electron-log.txt", 1500}, {"nw-log.txt", 600}, {"client.log", 400}}
	for _, l := range logs {
		section(fmt.Sprintf("Logs/%s (last %d lines)", l.name, l.lines))
		b.WriteString(tail(filepath.Join(paths.Logs(), l.name), l.lines))
	}

	if err := os.WriteFile(out, redact(b.Bytes()), 0o644); err != nil {
		return err
	}
	abs, _ := filepath.Abs(out)
	fmt.Printf("Wrote %s\n", abs)
	fmt.Println("Your home folder, user and host names and e-mail addresses are replaced in it; read it before you share it.")
	return nil
}

func osRelease() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if v, ok := strings.CutPrefix(s.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return "unknown"
}

// buildVersion is the commit ggctl (and so ggd, built with it) is from.
func buildVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, when, dirty := "", "", ""
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			when = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = " (modified)"
			}
		}
	}
	if rev == "" {
		return bi.GoVersion + ", commit unknown"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return fmt.Sprintf("%s %s%s, %s", rev, when, dirty, runtime.Version())
}

func command(prefix string, name string, args ...string) string {
	out, err := exec.Command(name, args...).CombinedOutput()
	s := strings.TrimRight(string(out), "\n")
	if err != nil && s == "" {
		s = err.Error()
	}
	return prefix + s + "\n"
}

func access(path string) string {
	switch {
	case unix.Access(path, unix.R_OK|unix.W_OK) == nil:
		return "read/write"
	case unix.Access(path, unix.R_OK) == nil:
		return "read only"
	default:
		return "no access"
	}
}

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// tail is a file's last n lines.
func tail(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Sprintf("(%v)\n", err)
	}
	defer f.Close()
	// Read at most the last 4 MB: enough for n lines of these logs.
	if st, err := f.Stat(); err == nil && st.Size() > 4<<20 {
		f.Seek(-4<<20, io.SeekEnd)
	}
	b, _ := io.ReadAll(f)
	lines := strings.SplitAfter(string(b), "\n")
	if len(lines) > n+1 {
		lines = lines[len(lines)-n-1:]
	}
	return strings.Join(lines, "")
}

var emailRE = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

// redact replaces what identifies the user.
func redact(b []byte) []byte {
	s := string(b)
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		s = strings.ReplaceAll(s, home, "~")
	}
	if u, err := user.Current(); err == nil && len(u.Username) > 2 {
		s = regexp.MustCompile(`\b`+regexp.QuoteMeta(u.Username)+`\b`).ReplaceAllString(s, "<user>")
	}
	if h, err := os.Hostname(); err == nil && len(h) > 2 {
		s = regexp.MustCompile(`\b`+regexp.QuoteMeta(h)+`\b`).ReplaceAllString(s, "<host>")
	}
	s = emailRE.ReplaceAllStringFunc(s, func(m string) string {
		// Keep wss://default@127.0.0.1-style addresses in the client log.
		if strings.HasPrefix(m, "default@") {
			return m
		}
		return "<email>"
	})
	return []byte(s)
}
