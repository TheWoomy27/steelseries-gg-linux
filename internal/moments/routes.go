package moments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"ggl/internal/httpx"
)

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func decode[T any](r *http.Request) (T, error) {
	var v T
	err := httpx.Decode(r, &v)
	return v, err
}

func fail(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, errNoClip) {
		status = http.StatusNotFound
	} else if errors.Is(err, ErrNotRecording) {
		status = http.StatusConflict
	}
	httpx.Error(w, status, err.Error())
}

func (s *Service) routes() {
	sv := s.srv
	sv.Handle("GET /moments/sock", s.ws.ServeHTTP)

	sv.Handle("GET /moments/status", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"MomentsStatus": s.status()})
	})
	sv.Handle("POST /moments/enable", func(w http.ResponseWriter, r *http.Request) {
		if err := s.setEnabled(true); err != nil {
			fail(w, err)
			return
		}
		httpx.OK(w)
	})
	sv.Handle("POST /moments/disable", func(w http.ResponseWriter, r *http.Request) {
		if err := s.setEnabled(false); err != nil {
			fail(w, err)
			return
		}
		httpx.OK(w)
	})

	// Settings.
	sv.Handle("GET /moments/settings", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"momentsSettings": s.Settings()})
	})
	sv.Handle("POST /moments/settings", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		st, err := s.updateSettings(b)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.JSON(w, map[string]any{"updateMomentsSettings": st})
	})
	sv.Handle("GET /moments/settings/defaults", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"momentsSettings": defaultSettings()})
	})
	sv.Handle("POST /moments/settings/playClipSaveSound", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			Volume *int `json:"volume"`
		}](r)
		vol := s.Settings().ClipSaveSoundVolume
		if p.Volume != nil {
			vol = *p.Volume
		}
		playSound(filepath.Join(s.paths.Sounds, "clip-taken.wav"), vol)
		httpx.OK(w)
	})
	sv.Handle("GET /moments/settings/getAvailableMonitors", func(w http.ResponseWriter, r *http.Request) {
		type mon struct {
			Name      string `json:"name"`
			Primary   bool   `json:"primary"`
			ID        string `json:"id"`
			Thumbnail string `json:"monitor_thumbnail"`
		}
		out := []mon{}
		for _, m := range monitors(r.Context()) {
			name := m.Name
			if m.Description != "" {
				name = m.Description + " (" + m.Name + ")"
			}
			out = append(out, mon{Name: name, Primary: m.Primary, ID: m.Name, Thumbnail: monitorThumbnail(r.Context(), m)})
		}
		httpx.JSON(w, map[string]any{"availableMonitors": out})
	})
	sv.Handle("GET /moments/multimediadevices", func(w http.ResponseWriter, r *http.Request) {
		render, capture := multimediaDevices(r.Context())
		httpx.JSON(w, map[string]any{"multimedia_devices": map[string]any{"render_devices": render, "capture_devices": capture}})
	})
	sv.Handle("GET /moments/storage", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"storageInfo": storage(s.Settings().CaptureDirectory)})
	})
	sv.Handle("GET /moments/memoryInfo", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"memoryInfo": map[string]any{"total_ram": totalRAM()}})
	})
	sv.Handle("GET /moments/gsdkLongVersion", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"gsdkLongVersion": "linux"})
	})

	// Games.
	sv.Handle("GET /moments/games", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"MomentsGames": s.games()})
	})
	sv.Handle("POST /moments/setCaptureEnabled", func(w http.ResponseWriter, r *http.Request) {
		p, err := decode[struct {
			ID             int  `json:"id"`
			CaptureEnabled bool `json:"capture_enabled"`
		}](r)
		if err == nil {
			err = s.lib.setCaptureEnabled(p.ID, p.CaptureEnabled)
		}
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		s.mu.Lock()
		if s.gameRow.ID == p.ID {
			s.gameRow.CaptureEnabled = p.CaptureEnabled
		}
		s.mu.Unlock()
		s.reconcile()
		httpx.OK(w)
	})
	sv.Handle("GET /moments/autoclipNames", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"MomentsGamesenseGameNames": []any{}})
	})

	// Desktop mode records the desktop when no game is being recorded.
	sv.Handle("POST /moments/desktopMode/start", func(w http.ResponseWriter, r *http.Request) {
		s.setDesktopMode(true)
		httpx.OK(w)
	})
	sv.Handle("POST /moments/desktopMode/stop", func(w http.ResponseWriter, r *http.Request) {
		s.setDesktopMode(false)
		httpx.OK(w)
	})
	sv.Handle("GET /moments/desktopMode/settings", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		out := []map[string]string{}
		for k, v := range s.desktopS {
			out = append(out, map[string]string{"key": k, "value": v})
		}
		s.mu.Unlock()
		slices.SortFunc(out, func(a, b map[string]string) int { return strings.Compare(a["key"], b["key"]) })
		httpx.JSON(w, map[string]any{"get_desktopmode_settings": out})
	})
	sv.Handle("POST /moments/desktopMode/settings", func(w http.ResponseWriter, r *http.Request) {
		p, err := decode[struct {
			SettingID string `json:"settingId"`
			Enabled   any    `json:"enabled"`
		}](r)
		if err != nil || p.SettingID == "" {
			httpx.Error(w, http.StatusBadRequest, "settingId required")
			return
		}
		s.mu.Lock()
		s.desktopS[p.SettingID] = fmt.Sprint(p.Enabled)
		cp := map[string]string{}
		for k, v := range s.desktopS {
			cp[k] = v
		}
		s.mu.Unlock()
		s.lib.set("desktopModeSettings", cp)
		httpx.OK(w)
	})

	// The library.
	sv.Handle("GET /moments/clips", func(w http.ResponseWriter, r *http.Request) {
		clips, err := s.lib.clips()
		if err != nil {
			fail(w, err)
			return
		}
		httpx.JSON(w, map[string]any{"MomentsClips": clips})
	})
	sv.Handle("GET /moments/clips/session/{sessionId}", func(w http.ResponseWriter, r *http.Request) {
		clips, err := s.lib.clips()
		if err != nil {
			fail(w, err)
			return
		}
		id := r.PathValue("sessionId")
		out := []Clip{}
		for _, c := range clips {
			if c.SessionID == id {
				out = append(out, c)
			}
		}
		httpx.JSON(w, map[string]any{"MomentsClips": out})
	})
	sv.Handle("GET /moments/clips/currentSchemaVersion", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"effectsSchemaVersion": effectsSchemaVersion})
	})
	sv.Handle("POST /moments/clips/save", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		c, err := s.updateClip(b)
		if err != nil {
			fail(w, err)
			return
		}
		httpx.JSON(w, map[string]any{"clip": c})
	})
	sv.Handle("POST /moments/clips/delete", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		}](r)
		if err := s.deleteClip(p.ID); err != nil {
			fail(w, err)
			return
		}
		httpx.OK(w)
	})
	sv.Handle("POST /moments/clips/trim", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			ID string `json:"id"`
		}](r)
		c, err := s.trimClip(r.Context(), p.ID)
		if err != nil {
			fail(w, err)
			return
		}
		httpx.JSON(w, map[string]any{"clip": c})
	})
	sv.Handle("POST /moments/clips/export", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Session        string       `json:"session"`
			ID             string       `json:"id"`
			ClipStartPoint *float64     `json:"clip_start_point"`
			ClipEndPoint   *float64     `json:"clip_end_point"`
			Effects        []effect     `json:"effects"`
			Params         encodeParams `json:"params"`
			ExportPath     string       `json:"export_path"`
			TextBase64s    []textImage  `json:"textBase64s"`
		}
		if err := httpx.Decode(r, &p); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		c, out, err := s.exportClip(r.Context(), p.ID, p.ClipStartPoint, p.ClipEndPoint, p.Effects, p.TextBase64s, p.Params, p.ExportPath)
		if err != nil {
			fail(w, err)
			return
		}
		c.Path = out
		httpx.JSON(w, map[string]any{"clip": c})
	})
	sv.Handle("POST /moments/clips/screenshot", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			ID          string      `json:"id"`
			TimeStamp   float64     `json:"timestamp"`
			Effects     []effect    `json:"effects"`
			TextBase64s []textImage `json:"textBase64s"`
		}
		if err := httpx.Decode(r, &p); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := s.screenshot(r.Context(), p.ID, p.TimeStamp, p.Effects, p.TextBase64s)
		if err != nil {
			fail(w, err)
			return
		}
		httpx.JSON(w, map[string]any{"screenShotPath": out})
	})
	sv.Handle("POST /moments/clips/extractAudioTracks", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			ID      string `json:"id"`
			Indexes []struct {
				Index int `json:"index"`
			} `json:"indexes"`
		}](r)
		c, err := s.lib.clip(p.ID)
		if err != nil {
			fail(w, err)
			return
		}
		var idx []int
		for _, i := range p.Indexes {
			idx = append(idx, i.Index)
		}
		tracks, err := extractTracks(r.Context(), c, idx, filepath.Join(s.paths.Data, "audio-tracks"))
		if err != nil {
			httpx.JSON(w, map[string]any{"error": map[string]string{"message": err.Error()}})
			return
		}
		httpx.JSON(w, map[string]any{"clipAudioTracks": tracks})
	})
	sv.Handle("POST /moments/clips/copyURL", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			URL string `json:"url"`
		}](r)
		if err := copyToClipboard(p.URL); err != nil {
			fail(w, err)
			return
		}
		httpx.OK(w)
	})
	sv.Handle("POST /moments/createMontage", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			Session string   `json:"idSession"`
			ClipIDs []string `json:"clipIds"`
		}](r)
		c, err := s.createMontage(r.Context(), p.ClipIDs)
		if err != nil {
			fail(w, err)
			return
		}
		httpx.JSON(w, map[string]any{"clip": c})
	})
	// The shortcut (ggctl moments clip, or a compositor key binding).
	sv.Handle("POST /moments/clip", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Clip("shortcut", "")
		if err != nil {
			fail(w, err)
			return
		}
		httpx.JSON(w, map[string]any{"clip": c})
	})

	// The other Moments shortcuts.
	sv.Handle("POST /moments/toggle/{what}", func(w http.ResponseWriter, r *http.Request) {
		on, err := s.Toggle(r.PathValue("what"))
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.JSON(w, map[string]any{"on": on})
	})

	// Uploading: Windows GG uploads to YouTube through SteelSeries'
	// cloud. Here the clip is exported with its edits into the shared
	// clips folder and handed over: the file on the clipboard and the
	// service's upload page in the browser, to paste or drag it into.
	sv.Handle("POST /moments/upload", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			ClipID      string      `json:"clip_id"`
			Service     string      `json:"upload_service"`
			Title       string      `json:"title"`
			TextBase64s []textImage `json:"textBase64s"`
		}
		if err := httpx.Decode(r, &p); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		url, err := s.upload(r.Context(), p.ClipID, p.Service, p.Title, p.TextBase64s)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, context.Canceled) {
				status = 499
			}
			httpx.JSONStatus(w, status, map[string]any{"uploadClip": map[string]any{"url": "", "statusCode": status, "reasonForError": err.Error()}})
			return
		}
		httpx.JSON(w, map[string]any{"uploadClip": map[string]any{"url": url, "statusCode": http.StatusOK}})
	})
	sv.Handle("POST /moments/cancelUpload", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			ClipID  string `json:"clip_id"`
			Service string `json:"upload_service"`
		}](r)
		s.cancelUpload(p.ClipID, p.Service)
		httpx.OK(w)
	})
	sv.Handle("POST /moments/upload/clearLocalData", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })

	// Content and analytics.
	sv.Handle("GET /moments/promos/bucketContent/content", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"promosContent": []any{}})
	})
	sv.Handle("GET /moments/contentfulPreview", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, false) })
	sv.Handle("POST /moments/analytics/track", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	sv.Handle("GET /moments/contentViewHistory/{key}", func(w http.ResponseWriter, r *http.Request) {
		var v any
		if !s.lib.get("viewed:"+r.PathValue("key"), &v) {
			v = false
		}
		httpx.JSON(w, v)
	})
	sv.Handle("POST /moments/contentViewHistory/{key}", func(w http.ResponseWriter, r *http.Request) {
		s.lib.set("viewed:"+r.PathValue("key"), true)
		httpx.JSON(w, true)
	})
}

// games is GG's game library as Moments lists it, with each game's
// capture switch.
func (s *Service) games() []Game {
	out := []Game{}
	for _, g := range s.host.Games() {
		row, err := s.lib.gameID(g.UID, g.Name)
		if err != nil {
			continue
		}
		out = append(out, Game{ID: row.ID, Name: g.Name, Path: g.Exe, LauncherExecutables: []string{}, LibraryPath: g.Dir,
			LibraryGameUniqueID: g.UID, ScannerID: g.Scanner, CaptureEnabled: row.CaptureEnabled, IsInstalled: true, Options: map[string]any{}})
	}
	return out
}

// updateClip applies the editor's changes (MomentsUpdateClipRequest).
func (s *Service) updateClip(body []byte) (Clip, error) {
	var p struct {
		ID                   string            `json:"id"`
		Name                 *string           `json:"name"`
		Description          *string           `json:"description"`
		ClipStartPoint       *float64          `json:"clip_start_point"`
		ClipEndPoint         *float64          `json:"clip_end_point"`
		Tags                 []ClipTag         `json:"tags"`
		Effects              []json.RawMessage `json:"effects"`
		LastViewTimestamp    *string           `json:"last_view_timestamp"`
		EffectsSchemaVersion *int              `json:"effects_schema_version"`
		AudioTracksProps     []AudioTrackProps `json:"audio_tracks_props"`
		CropFormat           *string           `json:"crop_format"`
		CropTopLeft          *Point            `json:"crop_top_left"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Clip{}, err
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(body, &fields)
	c, err := s.lib.clip(p.ID)
	if err != nil {
		return c, err
	}
	edited := false
	if p.Name != nil {
		c.Name, edited = *p.Name, true
	}
	if p.Description != nil {
		c.Description, edited = *p.Description, true
	}
	if p.ClipStartPoint != nil {
		c.ClipStartPoint, edited = max(*p.ClipStartPoint, 0), true
	}
	if p.ClipEndPoint != nil {
		c.ClipEndPoint, edited = min(*p.ClipEndPoint, c.FullLength), true
	}
	if _, ok := fields["tags"]; ok {
		c.Tags = p.Tags
	}
	if _, ok := fields["effects"]; ok {
		c.Effects, edited = p.Effects, true
	}
	if p.LastViewTimestamp != nil {
		c.LastViewTimestamp = *p.LastViewTimestamp
	}
	if p.EffectsSchemaVersion != nil {
		c.EffectsSchemaVersion = *p.EffectsSchemaVersion
	}
	if _, ok := fields["audio_tracks_props"]; ok {
		c.AudioTracksProps, edited = p.AudioTracksProps, true
	}
	if p.CropFormat != nil {
		c.CropFormat, edited = *p.CropFormat, true
	}
	if _, ok := fields["crop_top_left"]; ok {
		c.CropTopLeft, edited = p.CropTopLeft, true
	}
	if edited {
		c.LastUserEditTimestamp = now()
	}
	return c, s.lib.put(c)
}

func (s *Service) deleteClip(id string) error {
	c, err := s.lib.clip(id)
	if err != nil {
		return err
	}
	if err := removeFile(c.Path); err != nil {
		return err
	}
	if c.TrimmedClipPath != "" {
		removeFile(c.TrimmedClipPath)
	}
	if c.ThumbnailPath != "" && strings.HasPrefix(c.ThumbnailPath, s.paths.Data) {
		os.Remove(c.ThumbnailPath)
	}
	tracks, _ := filepath.Glob(filepath.Join(s.paths.Data, "audio-tracks", c.ID+"_*"))
	for _, t := range tracks {
		os.Remove(t)
	}
	if err := s.lib.remove(id); err != nil {
		return err
	}
	s.ws.Broadcast("clipDeleted", map[string]string{"id": id})
	return nil
}

// trimClip makes a new clip of the part of a clip between its start and
// end points.
func (s *Service) trimClip(ctx context.Context, id string) (Clip, error) {
	c, err := s.lib.clip(id)
	if err != nil {
		return c, err
	}
	end := c.ClipEndPoint
	if end <= c.ClipStartPoint {
		end = c.FullLength
	}
	st := s.Settings()
	out := uniquePath(filepath.Join(st.TrimmedClipDirectory, safeName(c.Name)+" (trimmed).mp4"))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := trim(ctx, c.Path, out, c.ClipStartPoint, end, st.EncodingCrf); err != nil {
		return c, err
	}
	n := c
	n.ID, n.Path, n.TriggerType, n.IsManuallyTrimmed = newID(), out, "trimmed", true
	n.Name = c.Name
	n.ThumbnailPath, n.Keyframes = "", nil
	n.SharedClips, n.LastViewTimestamp, n.LastUserEditTimestamp = nil, "", ""
	// Effects keep their place in the new, shorter clip.
	n.Effects = shiftEffects(c.Effects, -c.ClipStartPoint)
	n.RecordingTimestamp = now()
	if err := s.addClip(ctx, &n); err != nil {
		return n, err
	}
	return n, nil
}

func shiftEffects(effects []json.RawMessage, by float64) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(effects))
	for _, raw := range effects {
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			out = append(out, raw)
			continue
		}
		for _, k := range []string{"start", "end"} {
			if v, ok := m[k].(float64); ok {
				m[k] = max(v+by, 0)
			}
		}
		out = append(out, mustJSON(m))
	}
	return out
}

func (s *Service) exportClip(ctx context.Context, id string, start, end *float64, effects []effect, texts []textImage, params encodeParams, exportPath string) (Clip, string, error) {
	c, err := s.lib.clip(id)
	if err != nil {
		return c, "", err
	}
	j := renderJob{clip: c, start: c.ClipStartPoint, end: c.ClipEndPoint, effects: effects, texts: texts, params: params}
	if start != nil {
		j.start = *start
	}
	if end != nil {
		j.end = *end
	}
	if j.end <= j.start {
		j.end = c.FullLength
	}
	st := s.Settings()
	out := exportPath
	switch {
	case out == "":
		out = filepath.Join(st.SharedClipDirectory, safeName(c.Name)+".mp4")
	case !strings.EqualFold(filepath.Ext(out), ".mp4"):
		out = filepath.Join(out, safeName(c.Name)+".mp4")
	}
	out = uniquePath(out)
	tmp, err := os.MkdirTemp("", "gg-moments-export")
	if err != nil {
		return c, "", err
	}
	defer os.RemoveAll(tmp)
	j.tmpDir = tmp
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if err := j.export(ctx, out); err != nil {
		return c, "", err
	}
	return c, out, nil
}

func (s *Service) screenshot(ctx context.Context, id string, at float64, effects []effect, texts []textImage) (string, error) {
	c, err := s.lib.clip(id)
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "gg-moments-shot")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	j := renderJob{clip: c, effects: effects, texts: texts, tmpDir: tmp}
	out := uniquePath(filepath.Join(s.Settings().CaptureDirectory, "Screenshots", fmt.Sprintf("%s %s.png", safeName(c.Name), time.Now().Format("2006-01-02 15-04-05"))))
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return out, j.screenshot(ctx, at, out)
}

func (s *Service) createMontage(ctx context.Context, ids []string) (Clip, error) {
	var clips []Clip
	for _, id := range ids {
		c, err := s.lib.clip(id)
		if err != nil {
			return Clip{}, err
		}
		clips = append(clips, c)
	}
	if len(clips) == 0 {
		return Clip{}, errors.New("no clips")
	}
	st := s.Settings()
	out := uniquePath(filepath.Join(st.TrimmedClipDirectory, "Montage "+time.Now().Format("2006-01-02 15-04-05")+".mp4"))
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if err := montage(ctx, clips, out, st.EncodingCrf); err != nil {
		return Clip{}, err
	}
	n := Clip{Path: out, Name: "Montage", TriggerType: "trimmed", IsManuallyTrimmed: true, RecordingTimestamp: now(),
		Game: clips[0].Game, LastGameName: clips[0].LastGameName, LibraryGameUniqueID: clips[0].LibraryGameUniqueID}
	if err := s.addClip(ctx, &n); err != nil {
		return n, err
	}
	return n, nil
}
