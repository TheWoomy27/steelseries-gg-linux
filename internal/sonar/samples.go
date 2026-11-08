package sonar

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"ggl/internal/httpx"
	"ggl/internal/paths"
)

// Audio samples, to try a channel's settings. A sample plays on its
// channel's device, looping. The microphone's sample is a recording of the
// user, played into the microphone channel beside the microphone while the
// channel is monitored on the chat device, so the user hears their voice
// with the microphone's effects. Windows plays samples with libVLC; here
// pw-play plays them and starts them again at the end (so pausing starts a
// sample over), and pw-record records the microphone, unprocessed, for up
// to a minute.

type sampleFile struct{ id, file string }

var renderSamples = []sampleFile{{"gaming", "Gaming.ogg"}, {"virtualSurround", "VirtualSurround.ogg"}, {"movie", "Movie.ogg"}, {"music", "Music.ogg"}}

var roleSamples = map[string][]sampleFile{
	"Game":        renderSamples,
	"ChatRender":  {{"chat", "Chat.ogg"}, {"chatNoisy", "Chat_Noisy.ogg"}},
	"Media":       renderSamples,
	"Aux":         renderSamples,
	"ChatCapture": {{"record", ""}},
}

const recordingLimit = time.Minute

func recordingPath() string { return filepath.Join(paths.State(), "sonar-recording.wav") }

func samplePath(f sampleFile) string {
	if f.file == "" {
		return recordingPath()
	}
	return filepath.Join(paths.Assets(), "sonar", "audio_samples", f.file)
}

type audioSample struct {
	Role      string `json:"role"`
	ID        string `json:"id"`
	IsPlaying bool   `json:"isPlaying"`
}

type samplePlayer struct {
	sample string
	stop   chan struct{} // closed to stop playing; nil while paused
	done   chan struct{}
}

type samples struct {
	mu        sync.Mutex
	players   map[string]*samplePlayer // by role
	recorder  *exec.Cmd
	recorded  chan struct{}
	stopTimer *time.Timer
}

// sampleList is a role's samples; the recording is listed once it exists.
func (s *Service) sampleList(role string) []audioSample {
	s.samples.mu.Lock()
	p := s.samples.players[role]
	s.samples.mu.Unlock()
	out := []audioSample{}
	for _, f := range roleSamples[role] {
		if f.file == "" {
			if _, err := os.Stat(recordingPath()); err != nil {
				continue
			}
		}
		out = append(out, audioSample{camel(role), f.id, p != nil && p.sample == f.id && p.stop != nil})
	}
	return out
}

// playPause starts a sample, or pauses or resumes the one already chosen
// (Sonar's PlayPause).
func (s *Service) playPause(role, id string) error {
	i := slices.IndexFunc(roleSamples[role], func(f sampleFile) bool { return f.id == id })
	if i < 0 {
		return fmt.Errorf("the given key '%s' was not present in the dictionary", id)
	}
	s.samples.mu.Lock()
	p := s.samples.players[role]
	s.samples.mu.Unlock()
	switch {
	case p != nil && p.sample == id && p.stop != nil:
		s.stopPlayer(role, false)
		s.samples.mu.Lock()
		s.samples.players[role] = &samplePlayer{sample: id}
		s.samples.mu.Unlock()
		return nil
	case p != nil && p.stop != nil:
		s.stopPlayer(role, false)
	}
	return s.startPlayer(role, roleSamples[role][i])
}

func (s *Service) startPlayer(role string, f sampleFile) error {
	c, _ := channelByRole(role)
	player := c.node + "_sample"
	args := []string{"-P", fmt.Sprintf("{ node.name = %s media.name = \"Sonar sample\" }", player)}
	if c.capture {
		s.stopRecording()
		args = []string{"-P", fmt.Sprintf("{ node.name = %s media.name = \"Sonar sample\" node.autoconnect = false }", player)}
		s.audio.setMicSample(player)
	} else {
		args = append(args, "--target", c.node)
	}
	p := &samplePlayer{sample: f.id, stop: make(chan struct{}), done: make(chan struct{})}
	s.samples.mu.Lock()
	if s.samples.players == nil {
		s.samples.players = map[string]*samplePlayer{}
	}
	s.samples.players[role] = p
	s.samples.mu.Unlock()
	go func() {
		defer close(p.done)
		for {
			cmd := pwCommand("pw-play", append(args, samplePath(f))...)
			if err := cmd.Start(); err != nil {
				return
			}
			began := time.Now()
			exited := make(chan struct{})
			go func() { cmd.Wait(); close(exited) }()
			select {
			case <-p.stop:
				cmd.Process.Kill()
				<-exited
				return
			case <-exited:
				if time.Since(began) < time.Second {
					return // the file did not play
				}
			}
		}
	}()
	return nil
}

// stopPlayer stops a role's sample; notify tells the client it stopped.
func (s *Service) stopPlayer(role string, notify bool) []audioSample {
	s.samples.mu.Lock()
	p := s.samples.players[role]
	delete(s.samples.players, role)
	s.samples.mu.Unlock()
	if p == nil {
		return []audioSample{}
	}
	if p.stop != nil {
		close(p.stop)
		<-p.done
	}
	if role == "ChatCapture" {
		s.audio.setMicSample("")
	}
	if notify {
		s.ws.Broadcast("SONAR_EVENT_PLAYER_STOPPED", camel(role))
	}
	return []audioSample{{camel(role), p.sample, false}}
}

func (s *Service) stopSamples() {
	for _, r := range channelRoles {
		s.stopPlayer(r, false)
	}
	s.stopRecording()
}

func (s *Service) isRecording() bool {
	s.samples.mu.Lock()
	defer s.samples.mu.Unlock()
	return s.samples.recorder != nil
}

// startRecording records the microphone channel's microphone.
func (s *Service) startRecording() bool {
	s.stopPlayer("ChatCapture", true)
	s.samples.mu.Lock()
	defer s.samples.mu.Unlock()
	if s.samples.recorder != nil {
		return true
	}
	mic := s.redirectionDevices()["Mic"]
	if mic == "" {
		return false
	}
	cmd := pwCommand("pw-record", "--target", mic, "-P", "{ node.name = sonar_recorder media.name = \"Sonar recording\" }", recordingPath())
	if err := cmd.Start(); err != nil {
		return false
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	s.samples.recorder, s.samples.recorded = cmd, done
	s.samples.stopTimer = time.AfterFunc(recordingLimit, s.stopRecording)
	return true
}

// stopRecording ends a recording; pw-record finishes the file on SIGINT.
func (s *Service) stopRecording() {
	s.samples.mu.Lock()
	cmd, done := s.samples.recorder, s.samples.recorded
	if t := s.samples.stopTimer; t != nil {
		t.Stop()
	}
	s.samples.recorder, s.samples.recorded, s.samples.stopTimer = nil, nil, nil
	s.samples.mu.Unlock()
	if cmd == nil {
		return
	}
	cmd.Process.Signal(syscall.SIGINT)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		cmd.Process.Kill()
		<-done
	}
	s.ws.Broadcast("SONAR_EVENT_RECORDING_STOPPED", nil)
}

func (s *Service) sampleRoutes() {
	sv := s.srv
	role := func(v string) (string, bool) {
		name, ok := enumName(v, roleNames)
		_, has := roleSamples[name]
		return name, ok && has
	}
	sv.Handle("GET /audioSamples/samples", func(w http.ResponseWriter, r *http.Request) {
		name, ok := role(r.URL.Query().Get("role"))
		if !ok {
			reply(w, nil, argErrorf("The value '%s' is not valid.", r.URL.Query().Get("role")))
			return
		}
		httpx.JSON(w, s.sampleList(name))
	})
	sv.Handle("PUT /audioSamples/{role}/{sampleId}", func(w http.ResponseWriter, r *http.Request) {
		name, ok := role(r.PathValue("role"))
		if !ok {
			reply(w, nil, argErrorf("The value '%s' is not valid.", r.PathValue("role")))
			return
		}
		if c, _ := channelByRole(name); !s.audio.isRunning(c) {
			httpx.JSONStatus(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("The audio player was not found for %s role", name)})
			return
		}
		if err := s.playPause(name, r.PathValue("sampleId")); err != nil {
			reply(w, nil, err)
			return
		}
		httpx.JSON(w, s.sampleList(name))
	})
	sv.Handle("PUT /audioSamples/stop/{role}", func(w http.ResponseWriter, r *http.Request) {
		name, ok := role(r.PathValue("role"))
		if !ok {
			reply(w, nil, argErrorf("The value '%s' is not valid.", r.PathValue("role")))
			return
		}
		httpx.JSON(w, s.stopPlayer(name, false))
	})
	sv.Handle("GET /audioSamples/isRecording", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, s.isRecording()) })
	sv.Handle("PUT /audioSamples/startRecord", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, s.startRecording()) })
	sv.Handle("PUT /audioSamples/stopRecord", func(w http.ResponseWriter, r *http.Request) {
		s.stopRecording()
		w.WriteHeader(http.StatusOK)
	})
}
