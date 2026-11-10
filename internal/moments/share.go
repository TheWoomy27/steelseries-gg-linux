package moments

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ggl/internal/notify"

	"github.com/godbus/dbus/v5"
)

// uploadPages are where each service takes a video in the browser.
var uploadPages = map[string]string{
	"youtube":  "https://www.youtube.com/upload",
	"tiktok":   "https://www.tiktok.com/tiktokstudio/upload?from=webapp",
	"reddit":   "https://www.reddit.com/submit",
	"twitter":  "https://x.com/compose/post",
	"facebook": "https://www.facebook.com/",
	"discord":  "https://discord.com/app",
}

var serviceNames = map[string]string{"youtube": "YouTube", "tiktok": "TikTok", "reddit": "Reddit", "twitter": "X", "facebook": "Facebook", "discord": "Discord"}

// Hand-off steps, replaceable in tests. Browsers' upload pages take a
// video dragged in or picked with their file dialog, not pasted, so the
// clip is shown selected in the file manager, and its path goes on the
// clipboard for the dialog's location field (Ctrl+L, Ctrl+V).
var (
	clipFile = func(path string) error {
		var cmd *exec.Cmd
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			cmd = exec.Command("wl-copy", "--", path)
		} else {
			cmd = exec.Command("xclip", "-selection", "clipboard")
			cmd.Stdin = strings.NewReader(path)
		}
		return cmd.Run()
	}
	showFile = func(path string) error {
		conn, err := dbus.ConnectSessionBus()
		if err != nil {
			return err
		}
		defer conn.Close()
		u := (&url.URL{Scheme: "file", Path: path}).String()
		return conn.Object("org.freedesktop.FileManager1", "/org/freedesktop/FileManager1").
			Call("org.freedesktop.FileManager1.ShowItems", 0, []string{u}, "").Err
	}
	openURL  = func(u string) error { return exec.Command("xdg-open", u).Start() }
	tellUser = func(summary, body string) { notify.Send("moments-share", summary, body, "") }
)

// uploads are the uploads running, by clip and service, to cancel.
type uploads struct {
	mu     sync.Mutex
	cancel map[string]context.CancelFunc
}

func (s *Service) cancelUpload(clipID, service string) {
	s.ups.mu.Lock()
	defer s.ups.mu.Unlock()
	if c := s.ups.cancel[clipID+"/"+service]; c != nil {
		c()
	}
}

// upload exports a clip with its edits for a service and hands it over:
// the file goes on the clipboard and the service's upload page opens.
// It returns the file's URL, which GG shows as the shared link.
func (s *Service) upload(ctx context.Context, clipID, service, title string, texts []textImage) (string, error) {
	c, err := s.lib.clip(clipID)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(ctx)
	key := clipID + "/" + service
	s.ups.mu.Lock()
	if s.ups.cancel == nil {
		s.ups.cancel = map[string]context.CancelFunc{}
	}
	s.ups.cancel[key] = cancel
	s.ups.mu.Unlock()
	defer func() {
		cancel()
		s.ups.mu.Lock()
		delete(s.ups.cancel, key)
		s.ups.mu.Unlock()
	}()

	var effects []effect
	for _, raw := range c.Effects {
		var e effect
		if json.Unmarshal(raw, &e) == nil {
			effects = append(effects, e)
		}
	}
	name := strings.TrimSpace(title)
	if name == "" {
		name = c.Name
	}
	out := filepath.Join(s.Settings().SharedClipDirectory, safeName(name)+".mp4")
	_, path, err := s.exportClip(ctx, clipID, nil, nil, effects, texts, encodeParams{}, out)
	if err != nil {
		return "", fmt.Errorf("exporting the clip: %w", err)
	}
	fileURL := (&url.URL{Scheme: "file", Path: path}).String()

	display := serviceNames[service]
	if display == "" {
		display = service
	}
	if err := clipFile(path); err != nil {
		slog.Warn("moments share: clipboard", "err", err)
	}
	if err := showFile(path); err != nil {
		slog.Warn("moments share: file manager", "err", err)
	}
	if page := uploadPages[service]; page != "" {
		if err := openURL(page); err != nil {
			slog.Warn("moments share: browser", "err", err)
		}
	}
	tellUser("Clip ready for "+display, fmt.Sprintf("Drag %s from the folder into %s, or pick it with its file dialog: the path is on the clipboard (Ctrl+L, Ctrl+V).", filepath.Base(path), display))
	slog.Info("moments share: clip handed over", "clip", clipID, "service", service, "file", path)

	if c, err = s.lib.clip(clipID); err == nil {
		if c.SharedClips == nil {
			c.SharedClips = map[string]SharedClip{}
		}
		c.SharedClips[service] = SharedClip{UploadService: service, URL: fileURL, Timestamp: time.Now().UTC().Format(time.RFC3339)}
		if err := s.lib.put(c); err != nil {
			slog.Warn("moments share: saving the clip", "err", err)
		}
	}
	return fileURL, nil
}
