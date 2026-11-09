package moments

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHost struct {
	mu        sync.Mutex
	recording []bool
}

func (h *fakeHost) Games() []LibraryGame {
	return []LibraryGame{{UID: "1_570", Name: "Dota 2", Scanner: 1}}
}
func (h *fakeHost) SetRecording(on bool) {
	h.mu.Lock()
	h.recording = append(h.recording, on)
	h.mu.Unlock()
}
func (h *fakeHost) states() []bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]bool(nil), h.recording...)
}
func (h *fakeHost) Notify(tag, summary, body string) {}

func TestSettingsApply(t *testing.T) {
	d := defaultSettings()
	if d.Enabled || d.ClipLength != 60 || d.VideoFps != 30 || d.VideoHeight != 1080 || d.VideoWidth != 1920 {
		t.Fatalf("defaults: %+v", d)
	}
	s, err := d.apply(json.RawMessage(`{"resolution_height":1440,"framerate":60,"clip_length":30,"unknown":1,"capture_directory":"/tmp/x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.VideoHeight != 1440 || s.VideoWidth != 2560 || s.VideoFps != 60 || s.ClipLength != 30 || s.TrimmedClipDirectory != "/tmp/x/Trimmed Clips" {
		t.Fatalf("applied: %+v", s)
	}
	if _, err := d.apply(json.RawMessage(`{"resolution_height":999}`)); err == nil {
		t.Fatal("accepted an unknown resolution")
	}
	if !recordingChanged(d, s) {
		t.Fatal("a new resolution should restart the recorder")
	}
}

func TestSafeName(t *testing.T) {
	if got := safeName(`Half-Life: Alyx #1 / 100%`); got != "Half-Life_ Alyx _1 _ 100_" {
		t.Fatalf("safeName = %q", got)
	}
}

func TestParseXrandr(t *testing.T) {
	ms := parseXrandr("Monitors: 2\n 0: +*DP-0 2560/597x1440/336+0+0  DP-0\n 1: +HDMI-0 1920/509x1080/286+2560+0  HDMI-0\n")
	if len(ms) != 2 || !ms[0].Primary || ms[1].X != 2560 || ms[1].Width != 1920 {
		t.Fatalf("monitors: %+v", ms)
	}
}

func waitFor(t *testing.T, what string, d time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestRecordClipEdit records a generated picture and two generated sounds,
// saves a clip and edits it the ways the client does.
func TestRecordClipEdit(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	if testing.Short() {
		t.Skip("records for several seconds")
	}
	os.Setenv("GG_LINUX_MOMENTS_X264", "1") // keep the GPU out of it
	dir := t.TempDir()
	host := &fakeHost{}
	s, err := New("", Paths{DB: filepath.Join(dir, "moments.db"), Data: filepath.Join(dir, "data"), BufferDir: filepath.Join(dir, "buffer")}, host)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	removeFile = os.Remove
	s.testSource = []string{"-re", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30"}
	s.testAudio = []audioSource{
		{Name: "Game", Args: []string{"-re", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000"}},
		{Name: "Microphone", Args: []string{"-re", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000"}},
	}
	capture := filepath.Join(dir, "clips")
	if _, err := s.updateSettings([]byte(`{"enabled":true,"clip_length":5,"capture_directory":"` + capture + `"}`)); err != nil {
		t.Fatal(err)
	}
	s.SetActive(true)
	if st := s.status(); st.Status != "enabled" {
		t.Fatalf("status before recording: %+v", st)
	}
	if _, err := s.Clip("shortcut", ""); err != ErrNotRecording {
		t.Fatalf("clip while not recording: %v", err)
	}
	s.setDesktopMode(true)
	waitFor(t, "the recorder", 10*time.Second, func() bool { return s.status().Status == "running" })
	if st := s.status(); st.Game.Name != desktopGameName || !st.DesktopModeRunning {
		t.Fatalf("desktop status: %+v", st)
	}
	time.Sleep(8 * time.Second)

	c, err := s.Clip("shortcut", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	info, err := probe(ctx, c.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("clip %s: %.2fs %dx%d %.0ffps, %d audio streams, keyframes %v", c.Path, info.Duration, info.Width, info.Height, info.FPS, info.AudioStreams, info.Keyframes)
	if info.Width != 640 || info.Duration < 4 || info.Duration > 6.5 {
		t.Errorf("clip is %.2fs long, want about 5", info.Duration)
	}
	if info.AudioStreams != 3 || len(c.AudioTracksProps) != 2 {
		t.Errorf("clip has %d audio streams and %d track props, want the mix and 2 sources", info.AudioStreams, len(c.AudioTracksProps))
	}
	if !strings.HasPrefix(c.Path, filepath.Join(capture, "Desktop")+"/") || c.ThumbnailPath == "" || c.Game.Name != desktopGameName {
		t.Errorf("clip: %+v", c)
	}
	if _, err := os.Stat(c.ThumbnailPath); err != nil {
		t.Error(err)
	}
	clips, _ := s.lib.clips()
	if len(clips) != 1 || clips[0].ID != c.ID {
		t.Fatalf("library: %+v", clips)
	}

	// The editor's changes, then a trim to them.
	if _, err := s.updateClip([]byte(`{"id":"` + c.ID + `","name":"Nice","clip_start_point":1,"clip_end_point":3.5,"audio_tracks_props":[{"name":"Game","volume":1,"muted":false},{"name":"Microphone","volume":0.5,"muted":true}]}`)); err != nil {
		t.Fatal(err)
	}
	tc, err := s.trimClip(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	ti, _ := probe(ctx, tc.Path)
	if ti.Duration < 2.2 || ti.Duration > 2.8 || ti.AudioStreams != 3 || tc.TriggerType != "trimmed" || tc.Name != "Nice" {
		t.Errorf("trimmed clip: %.2fs %d audio streams %+v", ti.Duration, ti.AudioStreams, tc)
	}

	_, out, err := s.exportClip(ctx, c.ID, nil, nil, nil, nil, encodeParams{TargetWidth: 320, TargetHeight: 180, TargetFramerate: 30}, "")
	if err != nil {
		t.Fatal(err)
	}
	ei, _ := probe(ctx, out)
	if ei.Width != 320 || ei.Height != 180 || ei.AudioStreams != 1 || ei.Duration < 2.2 || ei.Duration > 2.8 {
		t.Errorf("export %s: %+v", out, ei)
	}

	// A sharing service's size limit is a hard one.
	_, small, err := s.exportClip(ctx, c.ID, nil, nil, nil, nil, encodeParams{TargetFilesize: 400000}, "")
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(small); err != nil || fi.Size() > 400000 {
		t.Errorf("export over its size limit: %v %v", fi.Size(), err)
	}

	shot, err := s.screenshot(ctx, c.ID, 2, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(shot); err != nil || fi.Size() == 0 {
		t.Errorf("screenshot %s: %v", shot, err)
	}

	tracks, err := extractTracks(ctx, c, []int{0, 1}, filepath.Join(dir, "data", "audio-tracks"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range tracks {
		if _, err := os.Stat(tr.WaveFormPath); err != nil {
			t.Error(err)
		}
	}

	if err := s.deleteClip(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Path); !os.IsNotExist(err) {
		t.Errorf("deleted clip still there: %v", err)
	}

	s.setDesktopMode(false)
	waitFor(t, "the recorder to stop", 10*time.Second, func() bool {
		st := host.states()
		return len(st) >= 2 && !st[len(st)-1]
	})
	if st := s.status(); st.Status != "enabled" {
		t.Errorf("status after desktop mode: %+v", st)
	}
}

func TestScanLibrary(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	dir := t.TempDir()
	capture := filepath.Join(dir, "clips")
	os.MkdirAll(filepath.Join(capture, "Portal 2"), 0o755)
	os.MkdirAll(filepath.Join(capture, "Shared Clips"), 0o755)
	for _, p := range []string{filepath.Join(capture, "Portal 2", "old.mp4"), filepath.Join(capture, "Shared Clips", "shared.mp4")} {
		if err := ffmpeg(context.Background(), "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30:duration=1", "-c:v", "libx264", p); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New("", Paths{DB: filepath.Join(dir, "moments.db"), Data: filepath.Join(dir, "data"), BufferDir: filepath.Join(dir, "buffer")}, &fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	s.updateSettings([]byte(`{"capture_directory":"` + capture + `"}`))
	s.scanLibrary()
	clips, _ := s.lib.clips()
	if len(clips) != 1 || clips[0].Game.Name != "Portal 2" || clips[0].FullLength < 0.9 {
		t.Fatalf("scanned: %+v", clips)
	}
	games := s.games()
	if len(games) != 1 || games[0].ID == 0 || !games[0].CaptureEnabled {
		t.Fatalf("games: %+v", games)
	}
	if err := s.lib.setCaptureEnabled(games[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if g := s.games(); g[0].CaptureEnabled {
		t.Fatal("capture switch not saved")
	}
}

// TestExportEffects exports a clip cropped to 9:16 with a text the client
// drew over the whole frame.
func TestExportEffects(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	os.Setenv("GG_LINUX_MOMENTS_X264", "1")
	dir := t.TempDir()
	ctx := context.Background()
	src := filepath.Join(dir, "clip.mp4")
	if err := ffmpeg(ctx, "-f", "lavfi", "-i", "color=c=black:s=640x360:r=30:d=3", "-f", "lavfi", "-i", "sine=d=3",
		"-c:v", "libx264", "-c:a", "aac", "-shortest", src); err != nil {
		t.Fatal(err)
	}
	// A white square in the frame's top left corner, as a full-frame PNG.
	png := filepath.Join(dir, "text.png")
	if err := ffmpeg(ctx, "-f", "lavfi", "-i", "color=c=white@1:s=100x100,format=rgba", "-f", "lavfi", "-i", "color=c=black@0:s=640x360,format=rgba",
		"-filter_complex", "[1:v][0:v]overlay=0:0", "-frames:v", "1", png); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(png)
	s, err := New("", Paths{DB: filepath.Join(dir, "moments.db"), Data: filepath.Join(dir, "data"), BufferDir: filepath.Join(dir, "buffer")}, &fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	c := Clip{Path: src, Name: "fx", CropFormat: "9x16", CropTopLeft: &Point{X: 0, Y: 0}}
	if err := s.addClip(ctx, &c); err != nil {
		t.Fatal(err)
	}
	texts := []textImage{{EffectID: "t1", Base64: "data:image/png;base64," + base64.StdEncoding.EncodeToString(b)}}
	effects := []effect{{ID: "t1", Start: 0, End: 2, Type: json.RawMessage(`"text"`)}}
	_, out, err := s.exportClip(ctx, c.ID, nil, nil, effects, texts, encodeParams{}, filepath.Join(dir, "out.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := probe(ctx, out)
	if info.Width != 202 || info.Height != 360 {
		t.Fatalf("export is %dx%d, want 202x360", info.Width, info.Height)
	}
	// The square is in the crop (at its left edge) while the text shows,
	// and gone after.
	for _, c := range []struct {
		at   string
		want bool
	}{{"1", true}, {"2.5", false}} {
		px, err := runFF(ctx, "ffmpeg", "-v", "error", "-ss", c.at, "-i", out, "-frames:v", "1", "-vf", "crop=10:10:20:20,scale=1:1", "-f", "rawvideo", "-pix_fmt", "gray", "-")
		if err != nil || len(px) != 1 {
			t.Fatalf("pixel at %s: %v %v", c.at, px, err)
		}
		if white := px[0] > 200; white != c.want {
			t.Errorf("at %ss the text corner is %d, want white=%v", c.at, px[0], c.want)
		}
	}
}
