package moments

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Settings is moments_domains.MomentsSettings, what GET /moments/settings
// returns and POST /moments/settings changes.
type Settings struct {
	Enabled                     bool   `json:"enabled"`
	DisplayNotifications        bool   `json:"display_notifications"`
	EnableGameEventClips        bool   `json:"enable_game_event_clips"`
	IsMicMuted                  bool   `json:"is_mic_muted"`
	CaptureCursor               bool   `json:"capture_cursor"`
	ClipSaveSoundEnabled        bool   `json:"clip_save_sound_enabled"`
	ClipSaveSoundVolume         int    `json:"clip_save_sound_volume"`
	BrandExportedClips          bool   `json:"brand_exported_clips"`
	BrandOverlayPosition        string `json:"brand_overlay_position"`
	MomentsPostgame             bool   `json:"moments_postgame"`
	MomentsPostgameModal        bool   `json:"moments_postgame_modal"`
	EnableGamesenseOLED         bool   `json:"enable_gamesense_oled"`
	GamesenseCaptureStartedOLED bool   `json:"gamesense_capture_started_oled"`
	GamesenseClipSavedOLED      bool   `json:"gamesense_clip_saved_oled"`
	GamesenseMicMutedOLED       bool   `json:"gamesense_mic_muted_oled"`
	RecordingMode               int    `json:"recording_mode"`
	AudioBitRate                int    `json:"audio_bitrate"`
	AudioSampleRate             int    `json:"audio_samplerate"`
	CaptureDelayEvent           int    `json:"capture_delay_event"`
	CaptureDelayManual          int    `json:"capture_delay_manual"`
	CaptureDirectory            string `json:"capture_directory"`
	ClipLength                  int    `json:"clip_length"`
	TrimmedClipDirectory        string `json:"trimmed_clip_directory"`
	SharedClipDirectory         string `json:"shared_clip_directory"`
	EncodingCrf                 int    `json:"encoding_crf"`
	VideoFps                    int    `json:"framerate"`
	VideoHeight                 int    `json:"resolution_height"`
	VideoWidth                  int    `json:"resolution_width"`
	AudioOutputDevice           string `json:"audio_output_device"`
	AudioCommOutputDevice       string `json:"audio_comm_output_device"`
	AudioInputDevice            string `json:"audio_input_device"`
	SelectedMonitor             string `json:"selected_monitor"`
}

// Device ids Moments uses for "whatever the system default is".
const (
	deviceDefault     = "default"
	deviceDefaultComm = "defaultCommunication"
)

// The resolutions Moments offers, as width for each height.
var resolutions = map[int]int{720: 1280, 1080: 1920, 1440: 2560}

// defaultSettings are the Windows Moments defaults (moments_config.defaults),
// except that capture starts off: recording the screen and sound is
// something the user turns on.
func defaultSettings() Settings {
	s := Settings{
		Enabled:                     false,
		DisplayNotifications:        true,
		EnableGameEventClips:        true,
		CaptureCursor:               true,
		ClipSaveSoundEnabled:        true,
		ClipSaveSoundVolume:         100,
		BrandExportedClips:          true,
		BrandOverlayPosition:        "bottom-left",
		MomentsPostgame:             true,
		MomentsPostgameModal:        true,
		EnableGamesenseOLED:         true,
		GamesenseCaptureStartedOLED: true,
		GamesenseClipSavedOLED:      true,
		GamesenseMicMutedOLED:       true,
		AudioBitRate:                192,
		AudioSampleRate:             48000,
		CaptureDelayEvent:           5,
		CaptureDelayManual:          1,
		CaptureDirectory:            filepath.Join(videosDir(), "SteelSeries Moments"),
		ClipLength:                  60,
		EncodingCrf:                 25,
		VideoFps:                    30,
		VideoHeight:                 1080,
		VideoWidth:                  1920,
		AudioOutputDevice:           deviceDefault,
		AudioCommOutputDevice:       deviceDefaultComm,
		AudioInputDevice:            deviceDefault,
	}
	s.derive()
	return s
}

// derive fills the settings that follow from others.
func (s *Settings) derive() {
	s.TrimmedClipDirectory = filepath.Join(s.CaptureDirectory, "Trimmed Clips")
	s.SharedClipDirectory = filepath.Join(s.CaptureDirectory, "Shared Clips")
}

// apply merges a partial update (the fields the client sent) into s and
// checks the result.
func (s Settings) apply(patch json.RawMessage) (Settings, error) {
	cur, _ := json.Marshal(s)
	m := map[string]json.RawMessage{}
	json.Unmarshal(cur, &m)
	upd := map[string]json.RawMessage{}
	if err := json.Unmarshal(patch, &upd); err != nil {
		return s, err
	}
	for k, v := range upd {
		if _, known := m[k]; known && string(v) != "null" {
			m[k] = v
		}
	}
	merged, _ := json.Marshal(m)
	var out Settings
	if err := json.Unmarshal(merged, &out); err != nil {
		return s, err
	}
	if err := out.validate(); err != nil {
		return s, err
	}
	out.derive()
	return out, nil
}

func (s *Settings) validate() error {
	if w, ok := resolutions[s.VideoHeight]; !ok {
		return fmt.Errorf("unsupported resolution height %d", s.VideoHeight)
	} else {
		s.VideoWidth = w
	}
	if !slices.Contains([]int{30, 60}, s.VideoFps) {
		return fmt.Errorf("unsupported frame rate %d", s.VideoFps)
	}
	if s.ClipLength < 5 || s.ClipLength > 20*60 {
		return fmt.Errorf("clip length %d is out of range", s.ClipLength)
	}
	if s.CaptureDirectory == "" || !filepath.IsAbs(s.CaptureDirectory) {
		return fmt.Errorf("capture directory must be an absolute path")
	}
	s.ClipSaveSoundVolume = min(max(s.ClipSaveSoundVolume, 0), 100)
	s.CaptureDelayManual = min(max(s.CaptureDelayManual, 0), 10)
	s.CaptureDelayEvent = min(max(s.CaptureDelayEvent, 0), 30)
	s.EncodingCrf = min(max(s.EncodingCrf, 15), 40)
	return nil
}

// recordingChanged reports whether going from a to b changes what the
// recorder does.
func recordingChanged(a, b Settings) bool {
	return a.Enabled != b.Enabled || a.VideoFps != b.VideoFps || a.VideoHeight != b.VideoHeight ||
		a.ClipLength != b.ClipLength || a.EncodingCrf != b.EncodingCrf || a.CaptureCursor != b.CaptureCursor ||
		a.AudioBitRate != b.AudioBitRate || a.AudioSampleRate != b.AudioSampleRate ||
		a.AudioOutputDevice != b.AudioOutputDevice || a.AudioCommOutputDevice != b.AudioCommOutputDevice ||
		a.AudioInputDevice != b.AudioInputDevice || a.IsMicMuted != b.IsMicMuted || a.SelectedMonitor != b.SelectedMonitor
}

// videosDir is the user's XDG videos directory.
func videosDir() string {
	home, _ := os.UserHomeDir()
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	if f, err := os.Open(filepath.Join(cfg, "user-dirs.dirs")); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			v, ok := strings.CutPrefix(line, "XDG_VIDEOS_DIR=")
			if !ok {
				continue
			}
			v = strings.Trim(v, `"`)
			v = strings.ReplaceAll(v, "$HOME", home)
			if filepath.IsAbs(v) && v != home {
				return v
			}
		}
	}
	return filepath.Join(home, "Videos")
}
