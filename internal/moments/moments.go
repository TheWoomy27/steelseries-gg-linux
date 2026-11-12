// Package moments is GG's clip recorder and library (the Windows
// SteelSeriesMoments sub-app): a replay buffer of the game being played,
// saved as a clip on a shortcut, a library of clips with an editor, and
// exports. The client talks to it over HTTPS (/moments/...) and listens on
// its websocket (/moments/sock).
package moments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ggl/internal/events"
	"ggl/internal/httpx"
)

// LibraryGame is a game in GG's library.
type LibraryGame struct {
	UID, Name string
	Exe, Dir  string
	Scanner   int
}

// Host is what Moments needs from the rest of GG.
type Host interface {
	// Games lists GG's game library.
	Games() []LibraryGame
	// SetRecording shows in the tray whether Moments is recording.
	SetRecording(on bool)
	// Notify shows a desktop notification, which replaces an earlier one
	// with the same tag, unless GG's overlay of that name ("" for none)
	// is switched off.
	Notify(overlay, tag, summary, body string)
}

// Paths are where Moments keeps its files.
type Paths struct {
	DB        string // the clip library
	Data      string // thumbnails, extracted audio tracks
	Sounds    string // the clip saved sound
	BufferDir string // the replay buffer; empty for the default
}

type Service struct {
	srv   *httpx.Server
	ws    *events.Hub
	lib   *library
	paths Paths
	ups   uploads
	host  Host
	kick  chan struct{}
	ctx   context.Context
	stopf context.CancelFunc
	wg    sync.WaitGroup
	// scanMu keeps library scans one at a time.
	scanMu sync.Mutex

	mu       sync.Mutex
	settings Settings
	active   bool         // the sub-app is switched on
	game     *LibraryGame // the game being played
	gameRow  gameRow
	desktop  bool // desktop mode
	desktopS map[string]string
	rec      recorder
	recFor   string // what rec records: a game's uid or "desktop"
	recCfg   recConfig
	session  string
	failures int
	retryAt  time.Time
	saving   bool

	onCapture []func()

	// testSource and testAudio replace the screen and the sound devices in
	// tests.
	testSource []string
	testAudio  []audioSource
}

const desktopTarget = "desktop"

func New(token string, p Paths, host Host) (*Service, error) {
	lib, err := openLibrary(p.DB)
	if err != nil {
		return nil, err
	}
	if p.BufferDir == "" {
		p.BufferDir = bufferDir()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		srv: httpx.NewServer("moments", token), ws: events.NewHub("moments", ""), lib: lib, paths: p, host: host,
		kick: make(chan struct{}, 1), ctx: ctx, stopf: cancel, desktopS: map[string]string{},
	}
	s.settings = defaultSettings()
	var saved Settings
	if lib.get("settings", &saved) {
		if st, err := s.settings.apply(mustJSON(saved)); err == nil {
			s.settings = st
		}
	}
	lib.get("desktopModeSettings", &s.desktopS)
	s.routes()
	s.ws.OnAuthenticated(func(send func(string, any)) { send("momentsStatus", s.statusEvent()) })
	s.wg.Add(1)
	go s.loop()
	return s, nil
}

// Handler serves the Moments API.
func (s *Service) Handler() *httpx.Server { return s.srv }

// SetActive follows the sub-app being switched on or off in GG.
func (s *Service) SetActive(on bool) {
	s.mu.Lock()
	changed := s.active != on
	s.active = on
	s.mu.Unlock()
	if changed {
		if on {
			go s.scanLibrary()
		}
		s.reconcile()
		s.captureChanged()
	}
}

// OnCaptureChanged runs fn when capture is switched on or off.
func (s *Service) OnCaptureChanged(fn func()) {
	s.mu.Lock()
	s.onCapture = append(s.onCapture, fn)
	s.mu.Unlock()
}

func (s *Service) captureChanged() {
	s.mu.Lock()
	fns := s.onCapture
	s.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// Capturing reports whether Moments records games, so whether it needs to
// know which game is being played.
func (s *Service) Capturing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active && s.settings.Enabled
}

// GameChanged follows the game being played: g is nil when none is.
func (s *Service) GameChanged(g *LibraryGame) {
	s.mu.Lock()
	if g == nil && s.game == nil || g != nil && s.game != nil && g.UID == s.game.UID {
		s.mu.Unlock()
		return
	}
	s.game = g
	s.gameRow = gameRow{}
	if g != nil {
		row, err := s.lib.gameID(g.UID, g.Name)
		if err != nil {
			slog.Warn("moments: game", "game", g.Name, "err", err)
		}
		s.gameRow = row
		s.session = newID()
	}
	s.mu.Unlock()
	if g != nil {
		slog.Info("moments: game started", "game", g.Name, "capture", s.gameRow.CaptureEnabled)
	} else {
		slog.Info("moments: game stopped")
	}
	s.reconcile()
}

// Stop ends recording and closes the library.
func (s *Service) Stop() {
	s.stopf()
	s.wg.Wait()
	s.mu.Lock()
	rec := s.rec
	s.rec = nil
	s.mu.Unlock()
	if rec != nil {
		rec.stop()
	}
	s.lib.close()
}

func (s *Service) reconcile() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// target is what should be recorded now: a game's uid, the desktop, or
// nothing.
func (s *Service) target() string {
	if !s.active || !s.settings.Enabled {
		return ""
	}
	if s.game != nil && s.gameRow.CaptureEnabled {
		return s.game.UID
	}
	if s.desktop {
		return desktopTarget
	}
	return ""
}

// loop starts and stops the recorder as the state changes, and restarts it
// if it stops by itself.
func (s *Service) loop() {
	defer s.wg.Done()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		var exited <-chan struct{}
		s.mu.Lock()
		if s.rec != nil {
			exited = s.rec.done()
		}
		s.mu.Unlock()
		select {
		case <-s.ctx.Done():
			return
		case <-s.kick:
		case <-tick.C:
		case <-exited:
			s.mu.Lock()
			if s.rec != nil {
				slog.Warn("moments: recorder stopped by itself", "backend", s.rec.name())
				s.rec = nil
				s.recFor = ""
				s.failures++
				s.retryAt = time.Now().Add(time.Duration(min(s.failures, 6)) * 10 * time.Second)
			}
			s.mu.Unlock()
		}
		s.apply()
	}
}

func (s *Service) apply() {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	s.mu.Lock()
	want, st, src, audio := s.target(), s.settings, s.testSource, s.testAudio
	s.mu.Unlock()
	var cfg recConfig
	if want != "" {
		cfg = s.recConfig(ctx, st, src)
		if src != nil {
			cfg.Audio, cfg.Monitor = audio, monitor{}
		}
	}
	s.mu.Lock()
	if s.target() != want {
		s.mu.Unlock()
		s.reconcile()
		return
	}
	same := s.rec != nil && s.recFor == want && sameRecording(s.recCfg, cfg)
	if same || s.rec == nil && want == "" || s.saving {
		s.mu.Unlock()
		return
	}
	if s.rec == nil && time.Now().Before(s.retryAt) {
		s.mu.Unlock()
		return
	}
	old := s.rec
	s.rec, s.recFor = nil, ""
	s.mu.Unlock()
	if old != nil {
		old.stop()
	}
	var rec recorder
	if want != "" {
		var err error
		rec, err = startRecorder(cfg)
		if err != nil {
			slog.Error("moments: cannot record", "err", err)
			s.mu.Lock()
			s.failures++
			s.retryAt = time.Now().Add(time.Minute)
			s.mu.Unlock()
		} else {
			slog.Info("moments: recording", "what", want, "backend", rec.name(), "monitor", cfg.Monitor.Name, "audio", len(cfg.Audio))
		}
	}
	s.mu.Lock()
	s.rec, s.recFor, s.recCfg = rec, want, cfg
	if rec != nil {
		s.failures = 0
	}
	s.mu.Unlock()
	s.host.SetRecording(rec != nil)
	s.sendStatus()
}

func (s *Service) recConfig(ctx context.Context, st Settings, src []string) recConfig {
	return recConfig{
		FPS: st.VideoFps, Height: st.VideoHeight, Seconds: st.ClipLength + st.CaptureDelayManual + 2, CRF: st.EncodingCrf,
		Cursor: st.CaptureCursor, Monitor: pickMonitor(monitors(ctx), st.SelectedMonitor),
		AudioBitrate: st.AudioBitRate, SampleRate: st.AudioSampleRate, Audio: audioSources(ctx, st),
		Dir: s.paths.BufferDir, Source: src,
	}
}

func sameRecording(a, b recConfig) bool {
	if a.FPS != b.FPS || a.Height != b.Height || a.Seconds != b.Seconds || a.CRF != b.CRF || a.Cursor != b.Cursor ||
		a.Monitor != b.Monitor || a.AudioBitrate != b.AudioBitrate || a.SampleRate != b.SampleRate || len(a.Audio) != len(b.Audio) {
		return false
	}
	for i := range a.Audio {
		if a.Audio[i].Device != b.Audio[i].Device || a.Audio[i].Name != b.Audio[i].Name {
			return false
		}
	}
	return true
}

// status is moments_domains.MomentsStatus.
type status struct {
	Status             string `json:"status"`
	RecordingMode      int    `json:"recording_mode"`
	Game               Game   `json:"game"`
	WindowHandle       int    `json:"window_handle"`
	DesktopModeRunning bool   `json:"desktop_mode_running"`
}

func (s *Service) status() status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := status{Status: "disabled", RecordingMode: s.settings.RecordingMode, Game: Game{LauncherExecutables: []string{}, Options: map[string]any{}}}
	if s.active && s.settings.Enabled {
		st.Status = "enabled"
	}
	if s.rec != nil {
		st.Status = "running"
		switch {
		case s.recFor == desktopTarget:
			st.Game.Name = desktopGameName
			st.DesktopModeRunning = true
		case s.game != nil:
			st.Game.ID, st.Game.Name = s.gameRow.ID, s.game.Name
			st.Game.Path, st.Game.LibraryGameUniqueID, st.Game.ScannerID = s.game.Exe, s.game.UID, s.game.Scanner
			st.Game.CaptureEnabled, st.Game.IsInstalled = true, true
		}
	}
	return st
}

func (s *Service) statusEvent() map[string]any {
	st := s.status()
	return map[string]any{"status": st.Status, "gameName": st.Game.Name, "gameID": st.Game.ID,
		"recordingMode": st.RecordingMode, "desktopModeRunning": st.DesktopModeRunning}
}

func (s *Service) sendStatus() { s.ws.Broadcast("momentsStatus", s.statusEvent()) }

// Settings returns the current settings.
func (s *Service) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

func (s *Service) updateSettings(patch []byte) (Settings, error) {
	s.mu.Lock()
	old := s.settings
	st, err := old.apply(patch)
	if err != nil {
		s.mu.Unlock()
		return old, err
	}
	s.settings = st
	s.mu.Unlock()
	if err := s.lib.set("settings", st); err != nil {
		return st, err
	}
	s.ws.Broadcast("momentsSettings", st)
	if recordingChanged(old, st) {
		s.mu.Lock()
		s.retryAt = time.Time{}
		s.mu.Unlock()
		s.reconcile()
	}
	if old.Enabled != st.Enabled {
		s.sendStatus()
		s.captureChanged()
	}
	if old.CaptureDirectory != st.CaptureDirectory {
		go s.scanLibrary()
	}
	return st, nil
}

func (s *Service) setEnabled(on bool) error {
	_, err := s.updateSettings([]byte(fmt.Sprintf(`{"enabled":%t}`, on)))
	return err
}

// Toggle switches capture, the microphone or desktop mode (the Moments
// shortcuts) and returns the new state.
func (s *Service) Toggle(what string) (bool, error) {
	st := s.Settings()
	var on bool
	var summary string
	switch what {
	case "capture":
		on = !st.Enabled
		if err := s.setEnabled(on); err != nil {
			return false, err
		}
		summary = map[bool]string{true: "Moments capture on", false: "Moments capture off"}[on]
	case "mic":
		on = !st.IsMicMuted
		if _, err := s.updateSettings(mustJSON(map[string]bool{"is_mic_muted": on})); err != nil {
			return false, err
		}
		summary = map[bool]string{true: "Moments: microphone muted", false: "Moments: microphone unmuted"}[on]
	case "desktop":
		s.mu.Lock()
		on = !s.desktop
		s.mu.Unlock()
		s.setDesktopMode(on)
		summary = map[bool]string{true: "Moments desktop capture on", false: "Moments desktop capture off"}[on]
	default:
		return false, fmt.Errorf("unknown toggle %q", what)
	}
	if st.DisplayNotifications {
		// GG's overlays for these: capture coming on, and desktop mode
		// going on and off.
		overlay := ""
		switch {
		case what == "capture" && on:
			overlay = "momentsActive"
		case what == "desktop" && on:
			overlay = "desktopModeActivated"
		case what == "desktop":
			overlay = "desktopModeDeactivated"
		}
		s.host.Notify(overlay, "moments/"+what, summary, "")
	}
	return on, nil
}

func (s *Service) setDesktopMode(on bool) {
	s.mu.Lock()
	s.desktop = on
	s.retryAt = time.Time{}
	s.mu.Unlock()
	s.reconcile()
	s.sendStatus()
}

// ErrNotRecording is returned for a clip when nothing is being recorded.
var ErrNotRecording = errors.New("Moments is not recording")

// Clip saves the replay buffer as a clip (the clip shortcut). trigger is
// the clip's trigger type: "shortcut" for the shortcut, "auto" for game
// events.
func (s *Service) Clip(trigger, triggerName string) (Clip, error) {
	s.mu.Lock()
	rec, st, game, row, session, what := s.rec, s.settings, s.game, s.gameRow, s.session, s.recFor
	if rec == nil || s.saving {
		s.mu.Unlock()
		if rec == nil {
			return Clip{}, ErrNotRecording
		}
		return Clip{}, errors.New("a clip is already being saved")
	}
	s.saving = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.saving = false
		s.mu.Unlock()
		s.reconcile()
	}()
	if st.ClipSaveSoundEnabled {
		playSound(filepath.Join(s.paths.Sounds, "clip-taken.wav"), st.ClipSaveSoundVolume)
	}
	delay := st.CaptureDelayManual
	if trigger == "auto" {
		delay = st.CaptureDelayEvent
	}
	select {
	case <-time.After(time.Duration(delay) * time.Second):
	case <-s.ctx.Done():
		return Clip{}, s.ctx.Err()
	}
	gameName, uid := "Desktop", ""
	if what != desktopTarget && game != nil {
		gameName, uid = game.Name, game.UID
	}
	when := time.Now()
	out := uniquePath(filepath.Join(st.CaptureDirectory, safeName(gameName), safeName(gameName)+" "+when.Format("2006-01-02 15-04-05")+".mp4"))
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
	defer cancel()
	if err := rec.save(ctx, float64(st.ClipLength), out); err != nil {
		slog.Error("moments: saving clip", "err", err)
		return Clip{}, err
	}
	c := Clip{ID: newID(), Path: out, TriggerType: trigger, TriggerName: triggerName, RecordingTimestamp: when.UTC().Format(time.RFC3339Nano),
		LibraryGameUniqueID: uid, SessionID: session, CaptureMode: rec.name(), AudioBitrate: st.AudioBitRate * 1000, AudioSampleRate: st.AudioSampleRate}
	c.Name = gameName
	if what == desktopTarget {
		c.Game = Game{Name: desktopGameName}
	} else if game != nil {
		c.Game = Game{ID: row.ID, Name: game.Name, Path: game.Exe, LibraryPath: game.Dir, LibraryGameUniqueID: game.UID, ScannerID: game.Scanner, CaptureEnabled: row.CaptureEnabled, IsInstalled: true}
	}
	c.LastGameName = gameName
	tracks := []AudioTrackProps{}
	for _, a := range audioSourcesOf(rec, s) {
		tracks = append(tracks, AudioTrackProps{Name: a.Name, Volume: 1})
	}
	c.AudioTracksProps = tracks
	if err := s.addClip(ctx, &c); err != nil {
		return c, err
	}
	slog.Info("moments: clip saved", "path", out, "length", c.FullLength)
	if st.DisplayNotifications {
		s.host.Notify("clipCaptured", "moments/clip", "Clip saved", fmt.Sprintf("%s · %.0f s", gameName, c.FullLength))
	}
	return c, nil
}

// audioSourcesOf is the sources the running recording records. Clips with
// one source have only the mixed track, which the editor doesn't split.
func audioSourcesOf(rec recorder, s *Service) []audioSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.recCfg.Audio) < 2 {
		return nil
	}
	return s.recCfg.Audio
}

// addClip fills in a clip from its file, makes its thumbnail and adds it
// to the library.
func (s *Service) addClip(ctx context.Context, c *Clip) error {
	info, err := probe(ctx, c.Path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(c.Path)
	if err != nil {
		return err
	}
	c.FullLength = info.Duration
	c.ClipStartPoint, c.ClipEndPoint = 0, info.Duration
	c.Framerate = int(info.FPS + 0.5)
	c.VideoBitrate = info.VideoBitrate
	c.ResolutionWidth, c.ResolutionHeight = info.Width, info.Height
	c.FileResolutionWidth, c.FileResolutionHeight = info.Width, info.Height
	if info.AudioRate > 0 {
		c.AudioSampleRate = info.AudioRate
	}
	if info.AudioBitrate > 0 {
		c.AudioBitrate = info.AudioBitrate
	}
	// The editor's timeline ends at the last keyframe, so the list ends
	// with the clip's end.
	c.Keyframes = info.Keyframes
	if len(c.Keyframes) == 0 {
		c.Keyframes = []float64{0}
	}
	if last := c.Keyframes[len(c.Keyframes)-1]; last < info.Duration-0.01 {
		c.Keyframes = append(c.Keyframes, info.Duration)
	}
	// A file found in the folder with several tracks is taken to be laid
	// out like Moments' own: the mix, then each source.
	if len(c.AudioTracksProps) == 0 && info.AudioStreams > 2 {
		for i := 1; i < info.AudioStreams; i++ {
			c.AudioTracksProps = append(c.AudioTracksProps, AudioTrackProps{Name: fmt.Sprintf("Track %d", i), Volume: 1})
		}
	}
	c.Filesize = fi.Size()
	c.EffectsSchemaVersion = effectsSchemaVersion
	if c.ID == "" {
		c.ID = newID()
	}
	thumb := filepath.Join(s.paths.Data, "thumbs", c.ID+".jpg")
	if err := thumbnail(ctx, c.Path, thumb, info.Duration/2); err != nil {
		slog.Warn("moments: thumbnail", "clip", c.Path, "err", err)
	} else {
		c.ThumbnailPath = thumb
	}
	if err := s.lib.put(*c); err != nil {
		return err
	}
	s.ws.Broadcast("newClipCreated", c)
	return nil
}

// scanLibrary adds videos in the capture directory that the library
// doesn't know, as the Windows Moments does after a reinstall.
func (s *Service) scanLibrary() {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	st := s.Settings()
	found := s.lib.unknownVideos(st.CaptureDirectory, st.SharedClipDirectory, filepath.Join(st.CaptureDirectory, "Screenshots"))
	if len(found) == 0 {
		return
	}
	slog.Info("moments: adding clips found in the capture folder", "count", len(found))
	for i, p := range found {
		ctx, cancel := context.WithTimeout(s.ctx, time.Minute)
		fi, _ := os.Stat(p)
		game := filepath.Base(filepath.Dir(p))
		if filepath.Dir(p) == filepath.Clean(st.CaptureDirectory) {
			game = "Desktop"
		}
		c := Clip{Path: p, Name: strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)), TriggerType: "shortcut", LastGameName: game, Game: Game{Name: game}}
		if game == "Desktop" {
			c.Game.Name = desktopGameName
		}
		if filepath.Dir(p) == filepath.Clean(st.TrimmedClipDirectory) {
			c.TriggerType = "trimmed"
		}
		if fi != nil {
			c.RecordingTimestamp = fi.ModTime().UTC().Format(time.RFC3339Nano)
		}
		if err := s.addClip(ctx, &c); err != nil {
			slog.Warn("moments: scanning", "file", p, "err", err)
		}
		cancel()
		s.ws.Broadcast("momentsNewClipsScanned", map[string]int{"numClipsToScan": len(found), "numClipsScanned": i + 1})
		if s.ctx.Err() != nil {
			return
		}
	}
	s.ws.Broadcast("momentsClipScanFinished", map[string]any{})
}

// safeName makes a game name usable as a file name, and as a file:// URL
// path in the client.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '#', '%':
			return '_'
		}
		if r < 32 {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(strings.Trim(s, "."))
	if s == "" {
		return "Clip"
	}
	return s
}

// uniquePath adds a number to p if a file is already there.
func uniquePath(p string) string {
	if _, err := os.Stat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		q := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(q); err != nil {
			return q
		}
	}
}

// removeFile is how deleted clips go: to the desktop trash.
var removeFile = trash

// trash moves a file to the desktop trash, or deletes it when there is no
// trash.
func trash(path string) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	if gio, err := exec.LookPath("gio"); err == nil {
		if exec.Command(gio, "trash", path).Run() == nil {
			return nil
		}
	}
	return os.Remove(path)
}
