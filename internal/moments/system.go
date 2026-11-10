package moments

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// monitor is a screen Moments can record.
type monitor struct {
	Name                string
	Description         string
	X, Y, Width, Height int
	Primary             bool
}

// monitors lists the screens: Hyprland's outputs, or X11's monitors.
func monitors(ctx context.Context) []monitor {
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" && os.Getenv("WAYLAND_DISPLAY") != "" {
		if out, err := exec.CommandContext(ctx, "hyprctl", "-j", "monitors").Output(); err == nil {
			var ms []struct {
				Name        string  `json:"name"`
				Description string  `json:"description"`
				Width       int     `json:"width"`
				Height      int     `json:"height"`
				X           int     `json:"x"`
				Y           int     `json:"y"`
				Focused     bool    `json:"focused"`
				Scale       float64 `json:"scale"`
				Transform   int     `json:"transform"`
				Disabled    bool    `json:"disabled"`
			}
			if json.Unmarshal(out, &ms) == nil {
				var out []monitor
				for _, m := range ms {
					if m.Disabled {
						continue
					}
					w, h := m.Width, m.Height
					if m.Transform%2 == 1 { // turned a quarter
						w, h = h, w
					}
					out = append(out, monitor{Name: m.Name, Description: m.Description, X: m.X, Y: m.Y, Width: w, Height: h, Primary: m.X == 0 && m.Y == 0})
				}
				return out
			}
		}
	}
	if os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("DISPLAY") != "" {
		if out, err := exec.CommandContext(ctx, "xrandr", "--listmonitors").Output(); err == nil {
			return parseXrandr(string(out))
		}
	}
	return nil
}

// " 0: +*DP-0 2560/597x1440/336+0+0  DP-0"
var xrandrMonitor = regexp.MustCompile(`^\s*\d+:\s+\+?(\*?)(\S+)\s+(\d+)/\d+x(\d+)/\d+\+(-?\d+)\+(-?\d+)`)

func parseXrandr(s string) []monitor {
	var out []monitor
	for _, line := range strings.Split(s, "\n") {
		m := xrandrMonitor.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		w, _ := strconv.Atoi(m[3])
		h, _ := strconv.Atoi(m[4])
		x, _ := strconv.Atoi(m[5])
		y, _ := strconv.Atoi(m[6])
		out = append(out, monitor{Name: m[2], X: x, Y: y, Width: w, Height: h, Primary: m[1] == "*"})
	}
	if len(out) > 0 && !anyPrimary(out) {
		out[0].Primary = true
	}
	return out
}

func anyPrimary(ms []monitor) bool {
	for _, m := range ms {
		if m.Primary {
			return true
		}
	}
	return false
}

// pickMonitor returns the monitor named id, or the primary one.
func pickMonitor(ms []monitor, id string) monitor {
	for _, m := range ms {
		if m.Name == id {
			return m
		}
	}
	for _, m := range ms {
		if m.Primary {
			return m
		}
	}
	if len(ms) > 0 {
		return ms[0]
	}
	return monitor{}
}

// monitorThumbnail grabs a small picture of a screen as a data URL.
func monitorThumbnail(ctx context.Context, m monitor) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out []byte
	var err error
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		out, err = exec.CommandContext(ctx, "grim", "-o", m.Name, "-s", "0.15", "-t", "jpeg", "-q", "70", "-").Output()
	} else if d := os.Getenv("DISPLAY"); d != "" {
		out, err = exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "x11grab",
			"-video_size", fmt.Sprintf("%dx%d", m.Width, m.Height), "-i", fmt.Sprintf("%s+%d,%d", d, m.X, m.Y),
			"-frames:v", "1", "-vf", "scale=384:-2", "-f", "image2pipe", "-c:v", "mjpeg", "-").Output()
	}
	if err != nil || len(out) == 0 {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(out)
}

// paDevice is a PulseAudio sink or source as pactl lists it.
type paDevice struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Properties  map[string]string `json:"properties"`
	MonitorOf   string            `json:"monitor_of_sink"`
}

func pactlList(ctx context.Context, kind string) []paDevice {
	out, err := exec.CommandContext(ctx, "pactl", "-f", "json", "list", kind).Output()
	if err != nil {
		return nil
	}
	var ds []paDevice
	json.Unmarshal(out, &ds)
	return ds
}

func pactlDefault(ctx context.Context, what string) string {
	out, err := exec.CommandContext(ctx, "pactl", "get-default-"+what).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// multimediaDevice is moments_domains.MomentsMultimediaDevice.
type multimediaDevice struct {
	ID                     string `json:"id"`
	HardwareID             string `json:"hardware_id"`
	IsDefault              bool   `json:"is_default"`
	IsDefaultCommunication bool   `json:"is_default_communication"`
	FriendlyName           string `json:"friendly_name"`
}

// commSink is the sink chat goes to: Sonar's Chat channel when Sonar runs.
const commSink = "sonar_chat"

func multimediaDevices(ctx context.Context) (render, capture []multimediaDevice) {
	render, capture = []multimediaDevice{}, []multimediaDevice{}
	defSink, defSource := pactlDefault(ctx, "sink"), pactlDefault(ctx, "source")
	sinks := pactlList(ctx, "sinks")
	hasComm := false
	for _, d := range sinks {
		hasComm = hasComm || d.Name == commSink
	}
	for _, d := range sinks {
		render = append(render, multimediaDevice{ID: d.Name, HardwareID: d.Name, FriendlyName: d.Description,
			IsDefault: d.Name == defSink, IsDefaultCommunication: d.Name == commSink || (!hasComm && d.Name == defSink)})
	}
	for _, d := range pactlList(ctx, "sources") {
		if d.Properties["device.class"] == "monitor" || strings.HasSuffix(d.Name, ".monitor") {
			continue
		}
		capture = append(capture, multimediaDevice{ID: d.Name, HardwareID: d.Name, FriendlyName: d.Description,
			IsDefault: d.Name == defSource, IsDefaultCommunication: d.Name == defSource})
	}
	return render, capture
}

// audioSources turns the settings' devices into what to record: the
// output (what the default or chosen sink plays), the chat output when it
// is a different sink, and the microphone unless muted.
func audioSources(ctx context.Context, s Settings) []audioSource {
	names := map[string]string{}
	sinks := pactlList(ctx, "sinks")
	for _, d := range sinks {
		names[d.Name] = d.Description
	}
	for _, d := range pactlList(ctx, "sources") {
		names[d.Name] = d.Description
	}
	defSink := pactlDefault(ctx, "sink")
	var out []audioSource
	output := s.AudioOutputDevice
	if output == "" || output == deviceDefault || names[output] == "" {
		output = defSink
	}
	if output != "" {
		out = append(out, audioSource{Device: output + ".monitor", Name: trackGame})
	} else {
		out = append(out, audioSource{Device: "@DEFAULT_MONITOR@", Name: trackGame})
	}
	comm := s.AudioCommOutputDevice
	if comm == "" || comm == deviceDefaultComm || names[comm] == "" {
		comm = ""
		if _, ok := names[commSink]; ok {
			comm = commSink
		}
	}
	if comm != "" && comm != output {
		out = append(out, audioSource{Device: comm + ".monitor", Name: trackChat})
	}
	if !s.IsMicMuted {
		in := s.AudioInputDevice
		if in == "" || in == deviceDefault || names[in] == "" {
			in = "@DEFAULT_SOURCE@"
		}
		out = append(out, audioSource{Device: in, Name: trackMic})
	}
	return out
}

// Audio track names, as Windows Moments names them: the editor names and
// colours the tracks by them (game green, chat blue, mic orange).
const (
	trackGame = "Game"
	trackChat = "Chat"
	trackMic  = "Mic"
)

// trackNames maps the names earlier gg-linux versions gave tracks (after
// their devices) to Moments' own.
var trackNames = map[string]string{
	"Game and system": trackGame,
	"Gaming":          trackGame,
	"Media":           trackGame,
	"Chat":            trackChat,
	"Microphone":      trackMic,
}

// storageInfo is moments_domains.MomentsStorageInfo.
type storageInfo struct {
	FolderSize         int64 `json:"folder_size,string"`
	DriveAvailable     int64 `json:"drive_available,string"`
	TotalDriveCapacity int64 `json:"total_drive_capacity,string"`
}

func storage(dir string) storageInfo {
	var si storageInfo
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				si.FolderSize += fi.Size()
			}
		}
		return nil
	})
	// The directory may not exist yet: measure the nearest existing parent.
	for p := dir; ; p = filepath.Dir(p) {
		var st syscall.Statfs_t
		if syscall.Statfs(p, &st) == nil {
			si.DriveAvailable = int64(st.Bavail) * st.Bsize
			si.TotalDriveCapacity = int64(st.Blocks) * st.Bsize
			break
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return si
}

func totalRAM() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			kb, _ := strconv.ParseUint(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
			return kb * 1024
		}
	}
	return 0
}

// playSound plays a sound file at volume (0-100) without waiting.
func playSound(path string, volume int) {
	if volume <= 0 {
		return
	}
	if _, err := os.Stat(path); err != nil {
		return
	}
	cmd := exec.Command("pw-play", "--volume", strconv.FormatFloat(float64(volume)/100, 'f', 2, 64), path)
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

// copyToClipboard puts text on the clipboard (wl-copy on Wayland, xclip on
// X11).
func copyToClipboard(text string) error {
	var cmd *exec.Cmd
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		cmd = exec.Command("wl-copy", "--", text)
	} else {
		cmd = exec.Command("xclip", "-selection", "clipboard")
		cmd.Stdin = strings.NewReader(text)
	}
	return cmd.Run()
}
