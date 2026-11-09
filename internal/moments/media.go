package moments

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// mediaInfo is what ffprobe tells about a video file.
type mediaInfo struct {
	Duration      float64
	Width, Height int
	FPS           float64
	VideoBitrate  int // bits per second
	AudioStreams  int
	AudioRate     int
	AudioBitrate  int // bits per second, of the first audio stream
	Keyframes     []float64
}

func runFF(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 && len(msg) > 600 {
			msg = msg[i+1:]
		}
		return out, fmt.Errorf("%s: %w: %s", name, err, msg)
	}
	return out, nil
}

func ffmpeg(ctx context.Context, args ...string) error {
	_, err := runFF(ctx, "ffmpeg", append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}, args...)...)
	return err
}

func parseRate(s string) float64 {
	n, d, ok := strings.Cut(s, "/")
	a, _ := strconv.ParseFloat(n, 64)
	if !ok {
		return a
	}
	b, _ := strconv.ParseFloat(d, 64)
	if b == 0 {
		return 0
	}
	return a / b
}

func probe(ctx context.Context, path string) (mediaInfo, error) {
	out, err := runFF(ctx, "ffprobe", "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	if err != nil {
		return mediaInfo{}, err
	}
	var p struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType    string `json:"codec_type"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			AvgFrameRate string `json:"avg_frame_rate"`
			RFrameRate   string `json:"r_frame_rate"`
			BitRate      string `json:"bit_rate"`
			SampleRate   string `json:"sample_rate"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return mediaInfo{}, err
	}
	var m mediaInfo
	m.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			if m.Width != 0 {
				continue
			}
			m.Width, m.Height = s.Width, s.Height
			m.FPS = parseRate(s.AvgFrameRate)
			if m.FPS <= 0 || m.FPS > 1000 {
				m.FPS = parseRate(s.RFrameRate)
			}
			m.VideoBitrate, _ = strconv.Atoi(s.BitRate)
		case "audio":
			if m.AudioStreams == 0 {
				m.AudioRate, _ = strconv.Atoi(s.SampleRate)
				m.AudioBitrate, _ = strconv.Atoi(s.BitRate)
			}
			m.AudioStreams++
		}
	}
	if m.Width == 0 {
		return m, fmt.Errorf("%s has no video", path)
	}
	// Keyframe times, from the packet flags (no decoding needed).
	if out, err := runFF(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time,flags", "-of", "csv=p=0", path); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			t, flags, ok := strings.Cut(strings.TrimSpace(line), ",")
			if !ok || !strings.HasPrefix(flags, "K") {
				continue
			}
			if v, err := strconv.ParseFloat(t, 64); err == nil {
				m.Keyframes = append(m.Keyframes, v)
			}
		}
	}
	return m, nil
}

// encoder is the H.264 encoder Moments uses for edits and for recording
// when ffmpeg encodes: NVENC when the GPU has it, else x264.
var encoder = sync.OnceValue(func() string {
	if os.Getenv("GG_LINUX_MOMENTS_X264") == "" {
		err := ffmpeg(context.Background(), "-f", "lavfi", "-i", "color=s=256x144:d=0.2", "-c:v", "h264_nvenc", "-f", "null", "-")
		if err == nil {
			return "h264_nvenc"
		}
	}
	return "libx264"
})

// videoArgs are the encoder options for constant quality crf, or for
// bitrate (bits/s) when it is set; gop is the keyframe interval in frames
// (0 for the encoder's default).
func videoArgs(crf, bitrate, gop int) []string {
	enc := encoder()
	args := []string{"-c:v", enc, "-pix_fmt", "yuv420p"}
	if enc == "h264_nvenc" {
		args = append(args, "-preset", "p4", "-rc", "vbr")
		if bitrate > 0 {
			args = append(args, "-b:v", strconv.Itoa(bitrate), "-maxrate", strconv.Itoa(bitrate*3/2), "-bufsize", strconv.Itoa(bitrate*2))
		} else {
			args = append(args, "-cq", strconv.Itoa(crf), "-b:v", "0")
		}
	} else {
		args = append(args, "-preset", "veryfast")
		if bitrate > 0 {
			args = append(args, "-b:v", strconv.Itoa(bitrate), "-maxrate", strconv.Itoa(bitrate*3/2), "-bufsize", strconv.Itoa(bitrate*2))
		} else {
			args = append(args, "-crf", strconv.Itoa(crf))
		}
	}
	if gop > 0 {
		args = append(args, "-g", strconv.Itoa(gop), "-bf", "0")
	}
	return args
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }

func thumbnail(ctx context.Context, video, out string, at float64) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return ffmpeg(ctx, "-ss", ftoa(at), "-i", video, "-frames:v", "1", "-vf", "scale=640:-2", "-q:v", "4", out)
}

// trim re-encodes [start, end) of in into out, keeping every audio track.
func trim(ctx context.Context, in, out string, start, end float64, crf int) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	args := []string{"-ss", ftoa(start), "-i", in, "-t", ftoa(end - start), "-map", "0:v:0", "-map", "0:a?"}
	args = append(args, videoArgs(crf, 0, 0)...)
	args = append(args, "-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", out)
	return ffmpeg(ctx, args...)
}

// effect is one of a clip's edits (moments_domains.Effect).
type effect struct {
	ID    string          `json:"id"`
	Start float64         `json:"start"`
	End   float64         `json:"end"`
	Type  json.RawMessage `json:"type"`
	Data  json.RawMessage `json:"data"`
}

// textImage is a text effect the client rendered to a PNG.
type textImage struct {
	EffectID string `json:"effectId"`
	Base64   string `json:"base64"`
}

// encodeParams is moments_domains.ClipEncodingParams.
type encodeParams struct {
	TargetFilesize     int64 `json:"target_filesize"`
	TargetVideoBitrate int   `json:"target_video_bitrate"`
	TargetAudioBitrate int   `json:"target_audio_bitrate"`
	TargetCRFValue     int   `json:"target_crf_value"`
	TargetFramerate    int   `json:"target_framerate"`
	TargetWidth        int   `json:"target_width"`
	TargetHeight       int   `json:"target_height"`
	IsShared           bool  `json:"is_shared"`
}

// renderJob describes an export or a screenshot of a clip with its edits.
type renderJob struct {
	clip       Clip
	start, end float64
	effects    []effect
	texts      []textImage
	params     encodeParams
	tmpDir     string // where text images are written
}

// writeTexts saves the text overlays and returns them by effect id.
func (j *renderJob) writeTexts() (map[string]string, error) {
	out := map[string]string{}
	for i, t := range j.texts {
		b64 := t.Base64
		if _, data, ok := strings.Cut(b64, ","); ok && strings.HasPrefix(b64, "data:") {
			b64 = data
		}
		b, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("text effect %s: %w", t.EffectID, err)
		}
		p := filepath.Join(j.tmpDir, fmt.Sprintf("text%d.png", i))
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return nil, err
		}
		out[t.EffectID] = p
	}
	return out, nil
}

// videoFilter builds the video part of the filter graph for the job, from
// input [0:v] to [v]: the text overlays (inputs 1..n), the crop format and
// the output size and frame rate. Overlay times are relative to the trimmed
// start.
func (j *renderJob) videoFilter(overlays []string, overlayEffects []effect) string {
	var f []string
	chain := "[0:v]"
	step := func(filter string) {
		label := fmt.Sprintf("[v%d]", len(f))
		f = append(f, chain+filter+label)
		chain = label
	}
	// The client draws each text over the whole source frame, so texts go
	// on before the crop.
	for i := range overlays {
		e := overlayEffects[i]
		in := fmt.Sprintf("[%d:v]", i+1)
		scaled := fmt.Sprintf("[t%d]", i)
		f = append(f, in+"format=rgba"+scaled)
		label := fmt.Sprintf("[v%d]", len(f))
		f = append(f, fmt.Sprintf("%s%sscale2ref=w=iw:h=ih[ts%d][base%d]", scaled, chain, i, i))
		f = append(f, fmt.Sprintf("[base%d][ts%d]overlay=0:0:enable='between(t,%s,%s)'%s", i, i, ftoa(e.Start-j.start), ftoa(e.End-j.start), label))
		chain = label
	}
	if crop := cropFilter(j.clip); crop != "" {
		step(crop)
	}
	if w, h := j.params.TargetWidth, j.params.TargetHeight; w > 0 && h > 0 {
		step(fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2", w, h))
	}
	if j.params.TargetFramerate > 0 {
		step(fmt.Sprintf("fps=%d", j.params.TargetFramerate))
	}
	step("format=yuv420p")
	f[len(f)-1] = strings.TrimSuffix(f[len(f)-1], chain) + "[v]"
	return strings.Join(f, ";")
}

// cropFilter crops a clip to its crop format (the client's 9:16 vertical
// crop), with its top left corner where the user put it, in source pixels.
func cropFilter(c Clip) string {
	if c.CropFormat == "" || !strings.Contains(c.CropFormat, "9x16") {
		return ""
	}
	x, y := 0.0, 0.0
	if c.CropTopLeft != nil {
		x, y = c.CropTopLeft.X, c.CropTopLeft.Y
	}
	return fmt.Sprintf("crop=trunc(ih*9/16/2)*2:ih:min(%s\\,iw-trunc(ih*9/16/2)*2):%s", ftoa(x), ftoa(y))
}

// audioMix mixes a clip's own audio tracks the way its track settings say,
// into [a]. Clips Moments recorded carry the mix first and each source
// after it; the edit's volumes apply to the sources.
func audioMix(c Clip, info mediaInfo) (filter string, mapArg string) {
	if info.AudioStreams == 0 {
		return "", ""
	}
	props := c.AudioTracksProps
	offset := info.AudioStreams - len(props)
	if len(props) == 0 || offset < 0 {
		return "", "0:a:0"
	}
	var ins []string
	var parts []string
	for i, p := range props {
		if p.Muted || p.Name == "Silence" {
			continue
		}
		// Volume is a gain, as the editor plays it.
		label := fmt.Sprintf("[a%d]", i)
		parts = append(parts, fmt.Sprintf("[0:a:%d]volume=%s%s", i+offset, ftoa(p.Volume), label))
		ins = append(ins, label)
	}
	switch len(ins) {
	case 0:
		return "anullsrc=r=48000:cl=stereo[a]", "[a]"
	case 1:
		parts[0] = strings.TrimSuffix(parts[0], ins[0]) + "[a]"
	default:
		parts = append(parts, strings.Join(ins, "")+fmt.Sprintf("amix=inputs=%d:normalize=0[a]", len(ins)))
	}
	return strings.Join(parts, ";"), "[a]"
}

// overlayInputs pairs the job's text effects with their images.
func (j *renderJob) overlayInputs() ([]string, []effect, error) {
	images, err := j.writeTexts()
	if err != nil {
		return nil, nil, err
	}
	var paths []string
	var effs []effect
	for _, e := range j.effects {
		p, ok := images[e.ID]
		if !ok || e.End <= j.start || e.Start >= j.end {
			continue
		}
		paths = append(paths, p)
		effs = append(effs, e)
	}
	return paths, effs, nil
}

func (j *renderJob) export(ctx context.Context, out string) error {
	info, err := probe(ctx, j.clip.Path)
	if err != nil {
		return err
	}
	overlays, effs, err := j.overlayInputs()
	if err != nil {
		return err
	}
	args := []string{"-ss", ftoa(j.start), "-t", ftoa(j.end - j.start), "-i", j.clip.Path}
	for _, p := range overlays {
		args = append(args, "-loop", "1", "-t", ftoa(j.end-j.start), "-i", p)
	}
	graph := j.videoFilter(overlays, effs)
	af, amap := audioMix(j.clip, info)
	if strings.HasPrefix(af, "anullsrc") {
		graph += ";" + af
	} else if af != "" {
		graph += ";" + af
	}
	args = append(args, "-filter_complex", graph, "-map", "[v]")
	if amap != "" {
		args = append(args, "-map", amap)
	}
	ab := j.params.TargetAudioBitrate
	if ab <= 0 {
		ab = 192000
		if j.params.TargetFilesize > 0 {
			ab = 128000
		}
	}
	bitrate := j.params.TargetVideoBitrate
	capped := false
	if bitrate == 0 && j.params.TargetFilesize > 0 {
		// Fit the size limit (a sharing service's), leaving room for the
		// sound and the container.
		if amap == "" {
			ab = 0
		}
		bitrate = int(float64(j.params.TargetFilesize*8)*0.9/(j.end-j.start)) - ab
		bitrate = max(bitrate, 200000)
		capped = true
	}
	crf := j.params.TargetCRFValue
	if crf == 0 {
		crf = 23
	}
	args = append(args, videoArgs(crf, bitrate, 0)...)
	if capped {
		// No peaks over the average: the limit is a hard one.
		args = append(args, "-maxrate", strconv.Itoa(bitrate), "-bufsize", strconv.Itoa(bitrate))
	}
	if amap != "" {
		args = append(args, "-c:a", "aac", "-b:a", strconv.Itoa(ab), "-shortest")
	}
	args = append(args, "-movflags", "+faststart", out)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	slog.Debug("moments: export", "args", args)
	return ffmpeg(ctx, args...)
}

func (j *renderJob) screenshot(ctx context.Context, at float64, out string) error {
	j.start, j.end = at, at+0.1
	overlays, effs, err := j.overlayInputs()
	if err != nil {
		return err
	}
	args := []string{"-ss", ftoa(at), "-i", j.clip.Path}
	for _, p := range overlays {
		args = append(args, "-i", p)
	}
	args = append(args, "-filter_complex", strings.Replace(j.videoFilter(overlays, effs), "format=yuv420p", "format=rgb24", 1), "-map", "[v]", "-frames:v", "1", out)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return ffmpeg(ctx, args...)
}

// extractedTrack is moments_domains.MomentsExtractedAudioTrack.
type extractedTrack struct {
	Path         string `json:"path"`
	Index        int    `json:"index"`
	WaveFormPath string `json:"waveFormPath"`
}

// extractTracks writes the clip's source tracks (the ones its track
// settings list) as audio files with a waveform picture each, reusing
// earlier results.
func extractTracks(ctx context.Context, c Clip, indexes []int, dir string) ([]extractedTrack, error) {
	info, err := probe(ctx, c.Path)
	if err != nil {
		return nil, err
	}
	offset := max(info.AudioStreams-len(c.AudioTracksProps), 0)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	out := []extractedTrack{}
	for _, i := range indexes {
		stream := i + offset
		if stream >= info.AudioStreams {
			return nil, fmt.Errorf("clip has no audio track %d", i)
		}
		base := filepath.Join(dir, fmt.Sprintf("%s_%d", c.ID, i))
		t := extractedTrack{Path: base + ".m4a", Index: i, WaveFormPath: base + ".png"}
		if _, err := os.Stat(t.Path); err != nil {
			if err := ffmpeg(ctx, "-i", c.Path, "-map", fmt.Sprintf("0:a:%d", stream), "-vn", "-c:a", "copy", t.Path); err != nil {
				return nil, err
			}
		}
		if _, err := os.Stat(t.WaveFormPath); err != nil {
			err := ffmpeg(ctx, "-i", t.Path, "-filter_complex", "aformat=channel_layouts=mono,showwavespic=s=1800x140:colors=white:scale=sqrt", "-frames:v", "1", t.WaveFormPath)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// montage joins clips (each from its start to end point) into one video at
// the first clip's size.
func montage(ctx context.Context, clips []Clip, out string, crf int) error {
	if len(clips) == 0 {
		return fmt.Errorf("no clips")
	}
	w, h := clips[0].FileResolutionWidth, clips[0].FileResolutionHeight
	if w == 0 || h == 0 {
		w, h = 1920, 1080
	}
	var args []string
	var graph []string
	var concatIn string
	for i, c := range clips {
		end := c.ClipEndPoint
		if end <= c.ClipStartPoint {
			end = c.FullLength
		}
		args = append(args, "-ss", ftoa(c.ClipStartPoint), "-t", ftoa(end-c.ClipStartPoint), "-i", c.Path)
		graph = append(graph,
			fmt.Sprintf("[%d:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:black,setsar=1,fps=60,format=yuv420p[v%d]", i, w, h, w, h, i),
			fmt.Sprintf("[%d:a:0]aformat=sample_rates=48000:channel_layouts=stereo[a%d]", i, i))
		concatIn += fmt.Sprintf("[v%d][a%d]", i, i)
	}
	graph = append(graph, fmt.Sprintf("%sconcat=n=%d:v=1:a=1[v][a]", concatIn, len(clips)))
	args = append(args, "-filter_complex", strings.Join(graph, ";"), "-map", "[v]", "-map", "[a]")
	args = append(args, videoArgs(crf, 0, 0)...)
	args = append(args, "-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", out)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return ffmpeg(ctx, args...)
}
