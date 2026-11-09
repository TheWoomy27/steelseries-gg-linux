package moments

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Clip is moments_domains.MomentsClip.
type Clip struct {
	ID                    string                `json:"id"`
	Name                  string                `json:"name"`
	Description           string                `json:"description"`
	Path                  string                `json:"path"`
	ClipStartPoint        float64               `json:"clip_start_point"`
	ClipEndPoint          float64               `json:"clip_end_point"`
	TriggerType           string                `json:"trigger_type"`
	TriggerName           string                `json:"trigger_name"`
	IsManuallyTrimmed     bool                  `json:"is_manually_trimmed"`
	Framerate             int                   `json:"framerate"`
	AudioBitrate          int                   `json:"audio_bitrate"`
	AudioSampleRate       int                   `json:"audio_samplerate"`
	VideoBitrate          int                   `json:"video_bitrate"`
	ResolutionHeight      int                   `json:"resolution_height"`
	ResolutionWidth       int                   `json:"resolution_width"`
	FileResolutionHeight  int                   `json:"file_resolution_height"`
	FileResolutionWidth   int                   `json:"file_resolution_width"`
	RecordingTimestamp    string                `json:"recording_timestamp"`
	TrimmedClipPath       string                `json:"trimmed_clip_path"`
	LibraryGameUniqueID   string                `json:"library_game_unique_id"`
	Game                  Game                  `json:"game"`
	LastGameName          string                `json:"last_game_name"`
	Keyframes             []float64             `json:"keyframes"`
	FullLength            float64               `json:"full_length"`
	Filesize              int64                 `json:"filesize"`
	TrimmedFilesize       int64                 `json:"trimmed_filesize"`
	ThumbnailPath         string                `json:"thumbnail_path"`
	GamesenseEvents       []json.RawMessage     `json:"gamesense_events"`
	IsProcessing          bool                  `json:"is_processing"`
	IsDeleted             bool                  `json:"is_deleted"`
	SharedClips           map[string]SharedClip `json:"shared_clips"`
	Tags                  []ClipTag             `json:"tags"`
	Effects               []json.RawMessage     `json:"effects"`
	LastViewTimestamp     string                `json:"last_view_timestamp"`
	LastUserEditTimestamp string                `json:"last_user_edit_timestamp"`
	CropFormat            string                `json:"crop_format"`
	CropTopLeft           *Point                `json:"crop_top_left"`
	EffectsSchemaVersion  int                   `json:"effects_schema_version"`
	SessionID             string                `json:"session_id"`
	AudioTracksProps      []AudioTrackProps     `json:"audio_tracks_props"`
	AudioDrop             float64               `json:"audio_drop"`
	VideoDrop             float64               `json:"video_drop"`
	HdrEnabled            bool                  `json:"hdr_enabled"`
	DuplicatedFramesCount int                   `json:"duplicated_frames_count"`
	CaptureMode           string                `json:"capture_mode"`
	GSDKVersion           string                `json:"gsdk_version"`
	VideoAudioDiff        float64               `json:"video_audio_last_ts_diff"`
}

type SharedClip struct {
	UploadService string `json:"upload_service"`
	URL           string `json:"url"`
	URLCopied     bool   `json:"url_copied"`
	Timestamp     string `json:"timestamp"`
}

type ClipTag struct {
	Type  int    `json:"type"`
	Label string `json:"label"`
}

type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type AudioTrackProps struct {
	Name   string  `json:"name"`
	Volume float64 `json:"volume"`
	Muted  bool    `json:"muted"`
}

// Game is moments_domains.MomentsGame.
type Game struct {
	ID                  int            `json:"id"`
	Name                string         `json:"name"`
	Path                string         `json:"path"`
	LauncherExecutables []string       `json:"launcher_executables"`
	LibraryPath         string         `json:"library_path"`
	LibraryGameUniqueID string         `json:"library_game_unique_id"`
	ScannerID           int            `json:"scanner_id"`
	CaptureEnabled      bool           `json:"capture_enabled"`
	IsInstalled         bool           `json:"is_installed"`
	Options             map[string]any `json:"options"`
}

// desktopGameName is the name Moments gives clips of the desktop, which the
// client localizes.
const desktopGameName = "moments.desktopModeGameName"

// effectsSchemaVersion is the clip effects format the client writes.
const effectsSchemaVersion = 2

// normalize gives nil slices and maps their empty JSON form, which the
// client expects.
func (c *Clip) normalize() {
	if c.Keyframes == nil {
		c.Keyframes = []float64{}
	}
	if c.GamesenseEvents == nil {
		c.GamesenseEvents = []json.RawMessage{}
	}
	if c.SharedClips == nil {
		c.SharedClips = map[string]SharedClip{}
	}
	if c.Tags == nil {
		c.Tags = []ClipTag{}
	}
	if c.Effects == nil {
		c.Effects = []json.RawMessage{}
	}
	if c.AudioTracksProps == nil {
		c.AudioTracksProps = []AudioTrackProps{}
	}
	if c.Game.LauncherExecutables == nil {
		c.Game.LauncherExecutables = []string{}
	}
	if c.Game.Options == nil {
		c.Game.Options = map[string]any{}
	}
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// library is the clip and game database.
type library struct {
	db *sql.DB
}

func openLibrary(path string) (*library, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS clips (id TEXT PRIMARY KEY, path TEXT NOT NULL, recorded TEXT NOT NULL, data TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS clips_path ON clips(path)`,
		`CREATE TABLE IF NOT EXISTS games (id INTEGER PRIMARY KEY AUTOINCREMENT, uid TEXT UNIQUE NOT NULL, name TEXT NOT NULL DEFAULT '', capture_enabled INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &library{db: db}, nil
}

func (l *library) close() { l.db.Close() }

func (l *library) get(key string, v any) bool {
	var s string
	if l.db.QueryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&s) != nil {
		return false
	}
	return json.Unmarshal([]byte(s), v) == nil
}

func (l *library) set(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = l.db.Exec(`INSERT INTO kv(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(b))
	return err
}

func (l *library) clips() ([]Clip, error) {
	rows, err := l.db.Query(`SELECT data FROM clips ORDER BY recorded DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Clip{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		var c Clip
		if json.Unmarshal([]byte(s), &c) == nil {
			c.normalize()
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

var errNoClip = errors.New("no such clip")

func (l *library) clip(id string) (Clip, error) {
	var s string
	if err := l.db.QueryRow(`SELECT data FROM clips WHERE id = ?`, id).Scan(&s); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Clip{}, errNoClip
		}
		return Clip{}, err
	}
	var c Clip
	err := json.Unmarshal([]byte(s), &c)
	c.normalize()
	return c, err
}

func (l *library) put(c Clip) error {
	c.normalize()
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = l.db.Exec(`INSERT INTO clips(id, path, recorded, data) VALUES(?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET path = excluded.path, recorded = excluded.recorded, data = excluded.data`,
		c.ID, c.Path, c.RecordingTimestamp, string(b))
	return err
}

func (l *library) remove(id string) error {
	_, err := l.db.Exec(`DELETE FROM clips WHERE id = ?`, id)
	return err
}

func (l *library) knownPaths() map[string]bool {
	out := map[string]bool{}
	rows, err := l.db.Query(`SELECT path FROM clips`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out[p] = true
		}
	}
	return out
}

// gameRow is Moments' record of a game in GG's library: the id clips and
// the client refer to and whether to record it.
type gameRow struct {
	ID             int
	UID, Name      string
	CaptureEnabled bool
}

// gameID returns the Moments id of the game with GG unique id uid,
// adding the game the first time.
func (l *library) gameID(uid, name string) (gameRow, error) {
	if _, err := l.db.Exec(`INSERT INTO games(uid, name) VALUES(?, ?) ON CONFLICT(uid) DO UPDATE SET name = excluded.name`, uid, name); err != nil {
		return gameRow{}, err
	}
	r := gameRow{UID: uid}
	var on int
	err := l.db.QueryRow(`SELECT id, name, capture_enabled FROM games WHERE uid = ?`, uid).Scan(&r.ID, &r.Name, &on)
	r.CaptureEnabled = on != 0
	return r, err
}

func (l *library) setCaptureEnabled(id int, on bool) error {
	v := 0
	if on {
		v = 1
	}
	res, err := l.db.Exec(`UPDATE games SET capture_enabled = ? WHERE id = ?`, v, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no game %d", id)
	}
	return nil
}

// videoExts are the files a library scan picks up.
var videoExts = []string{".mp4", ".mkv", ".mov", ".webm"}

// unknownVideos lists the videos under dir the library doesn't know, such
// as clips recorded before the database was reset, except in skip.
func (l *library) unknownVideos(dir string, skip ...string) []string {
	known := l.knownPaths()
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(d.Name(), ".") || slices.Contains(skip, p)) {
				return filepath.SkipDir
			}
			return nil
		}
		if slices.Contains(videoExts, strings.ToLower(filepath.Ext(p))) && !known[p] {
			out = append(out, p)
		}
		return nil
	})
	if len(out) > 0 {
		slog.Debug("moments: unknown videos", "dir", dir, "count", len(out))
	}
	return out
}
