package moments

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveRing records the real screen and sound through the fallback
// recorder (wf-recorder or x11grab, and PulseAudio): it only runs when
// GG_LINUX_MOMENTS_LIVE is set, since it records the desktop.
func TestLiveRing(t *testing.T) {
	if os.Getenv("GG_LINUX_MOMENTS_LIVE") == "" {
		t.Skip("records the desktop; set GG_LINUX_MOMENTS_LIVE to run")
	}
	ctx := context.Background()
	dir := t.TempDir()
	st := defaultSettings()
	cfg := recConfig{FPS: 30, Height: 1080, Seconds: 10, CRF: 25, Cursor: true,
		Monitor: pickMonitor(monitors(ctx), ""), AudioBitrate: 192, SampleRate: 48000,
		Audio: audioSources(ctx, st), Dir: filepath.Join(dir, "buf")}
	rec, err := startRing(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.stop()
	t.Logf("recording %s with %s, sound %+v", cfg.Monitor.Name, rec.name(), cfg.Audio)
	// The first segment runs long while the encoder starts.
	time.Sleep(13 * time.Second)
	out := filepath.Join(dir, "clip.mp4")
	if err := rec.save(ctx, 6, out); err != nil {
		t.Fatal(err)
	}
	b, _ := runFF(ctx, "ffprobe", "-v", "error", "-show_entries", "stream=index,codec_type,width,height,start_time,duration", "-of", "compact", out)
	t.Logf("\n%s", b)
	info, err := probe(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Duration < 5 || info.Duration > 7.5 || info.AudioStreams != len(cfg.Audio)+1 {
		t.Errorf("clip: %+v", info)
	}
	if v := os.Getenv("GG_LINUX_MOMENTS_KEEP"); v != "" {
		copyFile(out, v)
	}
}
