package moments

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestUploadHandsOver exports a clip for YouTube into the shared clips
// folder, puts it on the clipboard, opens the upload page and marks the
// clip shared.
func TestUploadHandsOver(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	dir := t.TempDir()
	capture := filepath.Join(dir, "clips")
	os.MkdirAll(filepath.Join(capture, "Portal 2"), 0o755)
	if err := ffmpeg(context.Background(), "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30:duration=2", "-f", "lavfi", "-i", "sine=d=2",
		"-c:v", "libx264", "-c:a", "aac", "-shortest", filepath.Join(capture, "Portal 2", "jump.mp4")); err != nil {
		t.Fatal(err)
	}
	s, err := New("", Paths{DB: filepath.Join(dir, "moments.db"), Data: filepath.Join(dir, "data"), BufferDir: filepath.Join(dir, "buffer")}, &fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	s.updateSettings([]byte(`{"capture_directory":"` + capture + `"}`))
	s.scanLibrary()
	clips, _ := s.lib.clips()
	if len(clips) != 1 {
		t.Fatalf("clips: %+v", clips)
	}

	var clipped, opened, told string
	clipFile = func(p string) error { clipped = p; return nil }
	openURL = func(u string) error { opened = u; return nil }
	tellUser = func(summary, body string) { told = summary + ": " + body }
	url, err := s.upload(context.Background(), clips[0].ID, "youtube", "Big jump", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(capture, "Shared Clips", "Big jump.mp4")
	if clipped != want || url != "file://"+strings.ReplaceAll(want, " ", "%20") {
		t.Errorf("clipboard %q, url %q, want %q", clipped, url, want)
	}
	if info, err := probe(context.Background(), want); err != nil || info.Duration < 1.5 {
		t.Errorf("exported file: %+v %v", info, err)
	}
	if opened != "https://www.youtube.com/upload" || !strings.Contains(told, "YouTube") {
		t.Errorf("opened %q, told %q", opened, told)
	}
	c, _ := s.lib.clip(clips[0].ID)
	if sc := c.SharedClips["youtube"]; sc.URL != url || sc.Timestamp == "" {
		t.Errorf("shared clips: %+v", c.SharedClips)
	}
}
