package moments

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Moments keeps the last minute or so of the screen and sound in a replay
// buffer while a game runs (or the desktop, in desktop mode), and saves it
// as a clip when asked. On Windows its GSDK captures the game window. On
// Linux one of these records:
//
//   - gpu-screen-recorder, when installed: it keeps its own replay buffer
//     in memory, encodes on the GPU and records each sound source as its
//     own track; SIGUSR1 saves the buffer.
//   - wf-recorder (wlroots/Hyprland screencopy) or ffmpeg's x11grab (X11)
//     for the picture, with ffmpeg recording the sound and cutting
//     everything into one-second MPEG-TS segments in a ring in RAM; a clip
//     joins the newest segments.

// audioSource is a sound to record: a PulseAudio/PipeWire source name
// (sink monitors included) and the track name clips give it.
type audioSource struct {
	Device string
	Name   string
	// Args replaces the PulseAudio input, for tests: ffmpeg input
	// arguments producing sound in real time.
	Args []string
}

// recConfig is what a recorder records.
type recConfig struct {
	FPS          int
	Height       int // output height; the width follows the monitor's aspect
	Seconds      int // replay buffer length
	CRF          int
	Cursor       bool
	Monitor      monitor
	AudioBitrate int // kbit/s
	SampleRate   int
	Audio        []audioSource
	Dir          string // scratch space for the buffer
	// Source overrides the picture source, for tests: ffmpeg input
	// arguments producing video in real time.
	Source []string
}

// recorder is a running replay buffer.
type recorder interface {
	// save writes the buffer's last seconds to out (an .mp4).
	save(ctx context.Context, seconds float64, out string) error
	stop()
	// done is closed when the recorder has exited.
	done() <-chan struct{}
	name() string
}

// startRecorder starts the best recorder this session supports.
func startRecorder(cfg recConfig) (recorder, error) {
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, err
	}
	if cfg.Source == nil && os.Getenv("GG_LINUX_MOMENTS_BACKEND") != "ffmpeg" {
		if p, err := exec.LookPath("gpu-screen-recorder"); err == nil {
			return startGSR(p, cfg)
		}
	}
	return startRing(cfg)
}

// gsrRecorder runs gpu-screen-recorder in replay mode.
type gsrRecorder struct {
	cmd   *exec.Cmd
	dir   string
	saved chan string
	mu    sync.Mutex
	exit  chan struct{}
}

func startGSR(bin string, cfg recConfig) (recorder, error) {
	dir := filepath.Join(cfg.Dir, "gsr")
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	target := cfg.Monitor.Name
	if target == "" {
		target = "screen"
	}
	cursor := "no"
	if cfg.Cursor {
		cursor = "yes"
	}
	args := []string{"-w", target, "-f", strconv.Itoa(cfg.FPS), "-r", strconv.Itoa(cfg.Seconds),
		"-c", "mp4", "-k", "h264", "-q", gsrQuality(cfg.CRF), "-cursor", cursor,
		"-ac", "aac", "-ab", strconv.Itoa(cfg.AudioBitrate), "-o", dir}
	if cfg.Monitor.Height > cfg.Height && cfg.Height > 0 {
		w := cfg.Monitor.Width * cfg.Height / cfg.Monitor.Height
		args = append(args, "-s", fmt.Sprintf("%dx%d", w&^1, cfg.Height))
	}
	if len(cfg.Audio) > 0 {
		// The first track is everything mixed (what players play), then
		// each source on its own.
		var all []string
		for _, a := range cfg.Audio {
			all = append(all, gsrDevice(a.Device))
		}
		args = append(args, "-a", strings.Join(all, "|"))
		if len(cfg.Audio) > 1 {
			for _, a := range all {
				args = append(args, "-a", a)
			}
		}
	}
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = &logWriter{prefix: "gpu-screen-recorder"}
	slog.Info("moments: starting gpu-screen-recorder", "args", strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r := &gsrRecorder{cmd: cmd, dir: dir, saved: make(chan string, 4), exit: make(chan struct{})}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasSuffix(line, ".mp4") {
				select {
				case r.saved <- line:
				default:
				}
			}
		}
	}()
	go func() { cmd.Wait(); close(r.exit) }()
	return r, nil
}

// gsrQuality maps Moments' CRF onto gpu-screen-recorder's quality presets.
func gsrQuality(crf int) string {
	switch {
	case crf <= 20:
		return "ultra"
	case crf <= 24:
		return "very_high"
	case crf <= 28:
		return "high"
	}
	return "medium"
}

func gsrDevice(d string) string {
	switch d {
	case "@DEFAULT_MONITOR@":
		return "default_output"
	case "@DEFAULT_SOURCE@":
		return "default_input"
	}
	return "device:" + d
}

func (r *gsrRecorder) name() string          { return "gpu-screen-recorder" }
func (r *gsrRecorder) done() <-chan struct{} { return r.exit }

func (r *gsrRecorder) save(ctx context.Context, seconds float64, out string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(r.saved) > 0 {
		<-r.saved
	}
	if err := r.cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		return err
	}
	var path string
	select {
	case path = <-r.saved:
	case <-r.exit:
		return errors.New("gpu-screen-recorder exited")
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return errors.New("gpu-screen-recorder did not save the replay")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	info, err := probe(ctx, path)
	if err == nil && info.Duration > seconds+0.5 {
		// The buffer is longer than the clip asked for: keep its end.
		err = ffmpeg(ctx, "-sseof", ftoa(-seconds), "-i", path, "-map", "0", "-c", "copy", "-movflags", "+faststart", out)
		os.Remove(path)
		return err
	}
	if err := os.Rename(path, out); err != nil {
		err = ffmpeg(ctx, "-i", path, "-map", "0", "-c", "copy", out)
		os.Remove(path)
		return err
	}
	return nil
}

func (r *gsrRecorder) stop() {
	r.cmd.Process.Signal(syscall.SIGINT)
	select {
	case <-r.exit:
	case <-time.After(5 * time.Second):
		r.cmd.Process.Kill()
		<-r.exit
	}
	os.RemoveAll(r.dir)
}

// ringRecorder records one-second segments into a ring and joins the
// newest ones into clips.
type ringRecorder struct {
	dir   string
	procs []*exec.Cmd
	exit  chan struct{}
	mu    sync.Mutex
	label string
}

const segmentSeconds = 1

func startRing(cfg recConfig) (recorder, error) {
	dir := filepath.Join(cfg.Dir, "ring")
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	gop := cfg.FPS * segmentSeconds
	// The list keeps the segments of a full buffer; the files wrap later so
	// a clip being saved can still read the oldest ones.
	keep := cfg.Seconds/segmentSeconds + 3
	wrap := keep + 30

	var video *exec.Cmd
	var args []string
	label := "ffmpeg"
	scale := ""
	if cfg.Height > 0 && cfg.Monitor.Height > cfg.Height {
		scale = fmt.Sprintf("scale=-2:%d", cfg.Height)
	}
	switch {
	case cfg.Source != nil:
		args = append(args, cfg.Source...)
		label = "ffmpeg (test source)"
	case os.Getenv("WAYLAND_DISPLAY") != "":
		wf, err := exec.LookPath("wf-recorder")
		if err != nil {
			return nil, errors.New("recording on Wayland needs gpu-screen-recorder or wf-recorder")
		}
		wargs := []string{"-y", "-r", strconv.Itoa(cfg.FPS), "-m", "mpegts", "-f", "/dev/stdout", "-b", "0"}
		if cfg.Monitor.Name != "" {
			wargs = append(wargs, "-o", cfg.Monitor.Name)
		}
		if encoder() == "h264_nvenc" {
			wargs = append(wargs, "-c", "h264_nvenc", "-p", "preset=p4", "-p", "rc=vbr", "-p", "cq="+strconv.Itoa(cfg.CRF), "-p", "g="+strconv.Itoa(gop))
		} else {
			wargs = append(wargs, "-c", "libx264", "-p", "preset=veryfast", "-p", "crf="+strconv.Itoa(cfg.CRF), "-p", "g="+strconv.Itoa(gop))
		}
		if scale != "" {
			wargs = append(wargs, "-F", scale)
		}
		video = exec.Command(wf, wargs...)
		video.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
		video.Stderr = &logWriter{prefix: "wf-recorder"}
		args = append(args, "-use_wallclock_as_timestamps", "1", "-thread_queue_size", "1024", "-f", "mpegts", "-i", "pipe:0")
		label = "wf-recorder + ffmpeg"
	default:
		display := os.Getenv("DISPLAY")
		if display == "" {
			return nil, errors.New("no display to record")
		}
		draw := "0"
		if cfg.Cursor {
			draw = "1"
		}
		m := cfg.Monitor
		args = append(args, "-thread_queue_size", "1024", "-f", "x11grab", "-framerate", strconv.Itoa(cfg.FPS), "-draw_mouse", draw)
		if m.Width > 0 {
			args = append(args, "-video_size", fmt.Sprintf("%dx%d", m.Width, m.Height), "-i", fmt.Sprintf("%s+%d,%d", display, m.X, m.Y))
		} else {
			args = append(args, "-i", display)
		}
		label = "ffmpeg x11grab"
	}
	for _, a := range cfg.Audio {
		if cfg.Source == nil {
			// Line the sound up with the picture by their capture times,
			// which both take from the wall clock.
			args = append(args, "-isync", "0")
		}
		if a.Args != nil {
			args = append(args, a.Args...)
			continue
		}
		args = append(args, "-thread_queue_size", "1024", "-f", "pulse", "-sample_rate", strconv.Itoa(cfg.SampleRate), "-channels", "2", "-i", a.Device)
	}
	args = append(args, "-map", "0:v:0")
	if video != nil {
		args = append(args, "-c:v", "copy")
	} else {
		if scale != "" {
			args = append(args, "-vf", scale)
		}
		args = append(args, videoArgs(cfg.CRF, 0, gop)...)
		args = append(args, "-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", segmentSeconds))
	}
	if n := len(cfg.Audio); n > 0 {
		ab := strconv.Itoa(cfg.AudioBitrate) + "k"
		if n == 1 {
			args = append(args, "-map", "1:a")
		} else {
			// The mix first, then each source.
			var in string
			for i := range cfg.Audio {
				in += fmt.Sprintf("[%d:a]", i+1)
			}
			args = append(args, "-filter_complex", fmt.Sprintf("%samix=inputs=%d:normalize=0:dropout_transition=0[mix]", in, n), "-map", "[mix]")
			for i := range cfg.Audio {
				args = append(args, "-map", fmt.Sprintf("%d:a", i+1))
			}
		}
		args = append(args, "-c:a", "aac", "-b:a", ab, "-ar", strconv.Itoa(cfg.SampleRate))
	}
	args = append(args, "-f", "segment", "-segment_time", strconv.Itoa(segmentSeconds), "-segment_format", "mpegts",
		"-segment_wrap", strconv.Itoa(wrap), "-segment_list", filepath.Join(dir, "list.csv"),
		"-segment_list_type", "csv", "-segment_list_size", strconv.Itoa(keep), filepath.Join(dir, "seg%05d.ts"))
	ff := exec.Command("ffmpeg", append([]string{"-hide_banner", "-loglevel", "warning", "-nostdin"}, args...)...)
	ff.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	ff.Stderr = &logWriter{prefix: "ffmpeg"}
	r := &ringRecorder{dir: dir, exit: make(chan struct{}), label: label}
	slog.Info("moments: starting recorder", "backend", label, "args", strings.Join(ff.Args[1:], " "))
	if video != nil {
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		video.Stdout, ff.Stdin = pw, pr
		err = video.Start()
		if err == nil {
			r.procs = append(r.procs, video)
			err = ff.Start()
		}
		pr.Close()
		pw.Close()
		if err != nil {
			r.kill()
			return nil, err
		}
	} else if err := ff.Start(); err != nil {
		return nil, err
	}
	r.procs = append(r.procs, ff)
	go func() {
		var wg sync.WaitGroup
		for _, p := range r.procs {
			wg.Add(1)
			go func(p *exec.Cmd) {
				p.Wait()
				// When one part stops the other has no purpose.
				r.kill()
				wg.Done()
			}(p)
		}
		wg.Wait()
		close(r.exit)
	}()
	return r, nil
}

func (r *ringRecorder) name() string          { return r.label }
func (r *ringRecorder) done() <-chan struct{} { return r.exit }

func (r *ringRecorder) kill() {
	for _, p := range r.procs {
		if p.Process != nil {
			p.Process.Signal(syscall.SIGINT)
		}
	}
}

func (r *ringRecorder) stop() {
	r.kill()
	select {
	case <-r.exit:
	case <-time.After(5 * time.Second):
		for _, p := range r.procs {
			if p.Process != nil {
				p.Process.Kill()
			}
		}
		<-r.exit
	}
	os.RemoveAll(r.dir)
}

type segment struct {
	file       string
	start, end float64
}

func (r *ringRecorder) segments() ([]segment, error) {
	f, err := os.Open(filepath.Join(r.dir, "list.csv"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil && len(rows) == 0 {
		return nil, err
	}
	var out []segment
	for _, row := range rows {
		if len(row) < 3 {
			continue
		}
		s, _ := strconv.ParseFloat(row[1], 64)
		e, _ := strconv.ParseFloat(row[2], 64)
		out = append(out, segment{filepath.Join(r.dir, row[0]), s, e})
	}
	return out, nil
}

func (r *ringRecorder) save(ctx context.Context, seconds float64, out string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	segs, err := r.segments()
	if err != nil || len(segs) == 0 {
		return errors.New("nothing recorded yet")
	}
	last := segs[len(segs)-1].end
	first := len(segs) - 1
	for first > 0 && last-segs[first-1].start <= seconds+0.01 {
		first--
	}
	// Copy the segments out before the ring can reuse their files.
	tmp, err := os.MkdirTemp(r.dir, "save")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	var list strings.Builder
	for i, s := range segs[first:] {
		p := filepath.Join(tmp, fmt.Sprintf("%05d.ts", i))
		if err := copyFile(s.file, p); err != nil {
			return err
		}
		fmt.Fprintf(&list, "file '%s'\n", p)
	}
	lp := filepath.Join(tmp, "list.txt")
	if err := os.WriteFile(lp, []byte(list.String()), 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return ffmpeg(ctx, "-f", "concat", "-safe", "0", "-i", lp, "-map", "0", "-c", "copy", "-movflags", "+faststart", out)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	o, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(o, in); err != nil {
		o.Close()
		return err
	}
	return o.Close()
}

// logWriter sends a tool's stderr to the log, a line at a time.
type logWriter struct {
	prefix string
	buf    []byte
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(w.buf[:i])); line != "" {
			slog.Info("moments: "+w.prefix, "msg", line)
		}
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > 4096 {
		w.buf = w.buf[:0]
	}
	return len(p), nil
}

// bufferDir is where the replay buffer lives: shared memory when it has
// room, as on Windows, where Moments keeps it in RAM.
func bufferDir() string {
	uid := strconv.Itoa(os.Getuid())
	for _, d := range []string{"/dev/shm", os.Getenv("XDG_RUNTIME_DIR")} {
		if d == "" {
			continue
		}
		var st syscall.Statfs_t
		if syscall.Statfs(d, &st) == nil && st.Bavail*uint64(st.Bsize) > 2<<30 {
			return filepath.Join(d, "steelseries-gg-moments-"+uid)
		}
	}
	cache, _ := os.UserCacheDir()
	return filepath.Join(cache, "steelseries-gg", "moments-buffer")
}
