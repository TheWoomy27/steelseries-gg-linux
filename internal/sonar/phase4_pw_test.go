package sonar

import (
	"encoding/binary"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestStreamModeOnPipeWire runs streamer mode on a private PipeWire
// instance with WirePlumber (see TestRoutingOnPipeWire), measuring what
// reaches the monitoring and streaming devices.
func TestStreamModeOnPipeWire(t *testing.T) {
	runtime, data := os.Getenv("GG_TEST_PIPEWIRE"), os.Getenv("GG_LINUX_DATA")
	if runtime == "" || data == "" {
		t.Skip("GG_TEST_PIPEWIRE or GG_LINUX_DATA not set")
	}
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "sonar"), 0o755)
	os.MkdirAll(filepath.Join(tmp, "state"), 0o755)
	if err := exec.Command("cp", "-r", filepath.Join(data, "sonar", "db-migrations"), filepath.Join(tmp, "sonar")).Run(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GG_LINUX_DATA", tmp)
	t.Setenv("PIPEWIRE_RUNTIME_DIR", runtime)

	node := func(name, class string) {
		out, err := pwCommand("pw-cli", "create-node", "adapter",
			`{ factory.name = support.null-audio-sink node.name = `+name+` media.class = `+class+` audio.position = [ FL FR ] object.linger = true }`).CombinedOutput()
		if err != nil {
			t.Fatalf("pw-cli: %v: %s", err, out)
		}
		t.Cleanup(func() { pwCommand("pw-cli", "destroy", name).Run() })
	}
	node("test_head", "Audio/Sink")
	// The microphone is the instance's test_headset_mic, a virtual source
	// the tone can be played into.
	time.Sleep(500 * time.Millisecond)

	s, err := New("test")
	if err != nil {
		t.Fatal(err)
	}
	s.SetActive(true)
	defer s.SetActive(false)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	call := func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
		}
		return resp.StatusCode, string(b)
	}

	tone := filepath.Join(tmp, "tone.wav")
	writeTone(t, tone, 1500*time.Millisecond)
	// level plays the tone into a node (a sink, or the test microphone)
	// and returns the level a device's monitor records, in dBFS.
	level := func(device, into string) float64 {
		t.Helper()
		time.Sleep(1500 * time.Millisecond) // let relinking settle
		// pw-record falls back to the default device for a missing one.
		if ids, _ := nodeIDs(); ids[device] == 0 {
			t.Fatalf("no device %s", device)
		}
		rec := filepath.Join(tmp, "rec.wav")
		os.Remove(rec)
		r := pwCommand("pw-record", "--target", device, "-P", "{ stream.capture.sink = true }", "--rate", "48000", "--channels", "2", "--format", "s16", rec)
		if err := r.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
		if into == "test_headset_mic" {
			p := pwCommand("pw-cat", "-p", "-P", "{ node.name = mic_tone node.autoconnect = false }", tone)
			p.Start()
			defer p.Process.Kill()
			time.Sleep(500 * time.Millisecond)
			for _, ch := range []string{"FL", "FR"} {
				if out, err := pwCommand("pw-link", "mic_tone:output_"+ch, "test_headset_mic:input_MONO").CombinedOutput(); err != nil {
					t.Fatalf("linking the microphone tone: %v: %s", err, out)
				}
			}
			p.Wait()
		} else {
			pwCommand("pw-cat", "-p", "--target", into, tone).Run()
		}
		r.Process.Signal(syscall.SIGINT)
		r.Wait()
		return rmsDB(t, rec)
	}
	near := func(what string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1 {
			t.Errorf("%s: %.1f dBFS, want %.1f", what, got, want)
		} else {
			t.Logf("%s: %.1f dBFS", what, got)
		}
	}
	silent := func(what string, got float64) {
		t.Helper()
		if got > -70 {
			t.Errorf("%s: %.1f dBFS, want silence", what, got)
		} else {
			t.Logf("%s: silent", what)
		}
	}

	// Fallback would take the virtual test microphone, which Sonar does not
	// list, for a missing device.
	call("PUT", "/fallbackSettings/isEnabled/false", "")
	call("POST", "/onboarding", `{"devices":{"game":"test_head","chatRender":"test_head","chatCapture":"test_headset_mic"}}`)
	classic := level("test_head", "sonar_game")
	t.Logf("classic Game on the headset: %.1f dBFS", classic)

	call("PUT", "/mode/stream", "")
	_, body := call("GET", "/streamRedirections", "")
	if !strings.Contains(body, `"streamRedirectionId":"monitoring","deviceId":"test_head"`) || !strings.Contains(body, `"streamRedirectionId":"mic","deviceId":"test_headset_mic"`) {
		t.Fatalf("stream mode should start on the classic devices: %s", body)
	}
	call("PUT", "/streamRedirections/streaming/deviceId/"+streamDevice, "")
	call("PUT", "/streamRedirections/streaming/redirections/game/isEnabled/true", "")

	near("Game on the headset", level("test_head", "sonar_game"), classic)
	near("Game on the stream", level(streamDevice, "sonar_game"), classic)

	call("PUT", "/volumeSettings/streamer/streaming/game/volume/0.5", "")
	near("Game on the stream at 0.5", level(streamDevice, "sonar_game"), classic-6)
	call("PUT", "/streamRedirections/monitoring/redirections/game/isEnabled/false", "")
	silent("Game left out of monitoring", level("test_head", "sonar_game"))

	mic := level(streamDevice, "test_headset_mic")
	if mic < -50 {
		t.Errorf("microphone on the stream: %.1f dBFS", mic)
	}
	silent("microphone not monitored", level("test_head", "test_headset_mic"))

	call("PUT", "/streamRedirections/isStreamMonitoringEnabled/true", "")
	near("listening to the stream", level("test_head", "sonar_game"), classic-6)

	call("PUT", "/streamRedirections/monitoring/deviceId/"+streamDevice, "")
	if _, body = call("GET", "/streamRedirections/isStreamMonitoringLocked", ""); !strings.HasPrefix(body, "true") {
		t.Errorf("monitoring the streaming device should lock stream monitoring: %s", body)
	}
	call("PUT", "/streamRedirections/monitoring/deviceId/test_head", "")

	call("PUT", "/mode/classic", "")
	near("back in classic mode", level("test_head", "sonar_game"), classic)
}

func rmsDB(t *testing.T, path string) float64 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 44 {
		t.Fatalf("recording %s: %v", path, err)
	}
	pcm := b[44:]
	n := len(pcm) / 2
	var sum float64
	from, to := n/4, n*3/4
	for i := from; i < to; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
		sum += v * v
	}
	r := math.Sqrt(sum / float64(to-from))
	if r == 0 {
		return -120
	}
	return 20 * math.Log10(r)
}
