package sonar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeHeadsets stands in for Engine: test_a is a wireless headset with a
// ChatMix dial.
type fakeHeadsets struct {
	mu        sync.Mutex
	connected bool
}

const testHeadsetUSB = 0x103822a1

func (f *fakeHeadsets) Headset(usb uint32) (Headset, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Headset{ChatMixDial: true, Wireless: true, Connected: f.connected}, usb == testHeadsetUSB
}

func (f *fakeHeadsets) ChatMixLevels(usb uint32) (int, int, bool) {
	return 100, 100, usb == testHeadsetUSB
}

func (f *fakeHeadsets) setConnected(on bool) {
	f.mu.Lock()
	f.connected = on
	f.mu.Unlock()
}

// TestPhase3OnPipeWire runs ChatMix, fallback devices and samples against
// a private PipeWire instance with WirePlumber (see TestRoutingOnPipeWire).
func TestPhase3OnPipeWire(t *testing.T) {
	runtime, data := os.Getenv("GG_TEST_PIPEWIRE"), os.Getenv("GG_LINUX_DATA")
	if runtime == "" || data == "" {
		t.Skip("GG_TEST_PIPEWIRE or GG_LINUX_DATA not set")
	}
	tmp := t.TempDir()
	for _, dir := range []string{"sonar/db-migrations", "assets/sonar/audio_samples"} {
		os.MkdirAll(filepath.Dir(filepath.Join(tmp, dir)), 0o755)
		if err := exec.Command("cp", "-r", filepath.Join(data, dir), filepath.Join(tmp, dir)).Run(); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(filepath.Join(tmp, "state"), 0o755)
	t.Setenv("GG_LINUX_DATA", tmp)
	t.Setenv("PIPEWIRE_RUNTIME_DIR", runtime)

	node := func(name, class, extra string) {
		out, err := pwCommand("pw-cli", "create-node", "adapter",
			`{ factory.name = support.null-audio-sink node.name = `+name+` media.class = `+class+` audio.position = [ FL FR ] object.linger = true `+extra+` }`).CombinedOutput()
		if err != nil {
			t.Fatalf("pw-cli: %v: %s", err, out)
		}
	}
	sink := func(name, extra string) { node(name, "Audio/Sink", extra) }
	destroy := func(name string) { pwCommand("pw-cli", "destroy", name).Run() }
	for _, n := range []string{"test_a", "test_b", "test_c", "test_mic"} {
		t.Cleanup(func() { destroy(n) })
	}
	sink("test_a", `device.vendor.id = "0x1038" device.product.id = "0x22a1"`)
	sink("test_b", "")
	node("test_mic", "Audio/Source", "")
	time.Sleep(500 * time.Millisecond)

	s, err := New("test")
	if err != nil {
		t.Fatal(err)
	}
	heads := &fakeHeadsets{connected: true}
	s.SetHeadsets(heads)
	s.SetActive(true)
	defer s.SetActive(false)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/sock", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	events := make(chan string, 256)
	go func() {
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			events <- string(msg)
		}
	}()

	call := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(200 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	waitEvent := func(name, contains string) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case e := <-events:
				if strings.Contains(e, `"event":"`+name+`"`) && strings.Contains(e, contains) {
					return
				}
			case <-timeout:
				t.Fatalf("no %s event containing %s", name, contains)
			}
		}
	}
	gameDevice := func() string { return s.redirectionDevices()["Game"] }
	linked := func(from, to string) bool {
		out, _ := pwCommand("pw-link", "-l").Output()
		section := ""
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, " ") {
				section = line
				continue
			}
			if strings.HasPrefix(section, from+":") && strings.Contains(line, "|-> "+to+":") {
				return true
			}
		}
		return false
	}

	// Onboarding starts Sonar's audio with Game and Chat on the headset.
	if code, body := call("POST", "/onboarding", `{"devices":{"game":"test_a","chatRender":"test_a","chatCapture":"test_mic"}}`); code != 200 {
		t.Fatalf("onboarding: %d %s", code, body)
	}
	waitFor("Sonar's channels", func() bool { return s.audio.isRunning(channels[0]) })

	t.Run("chatmix dial", func(t *testing.T) {
		_, body := call("GET", "/v1/chatMix", "")
		if !strings.Contains(body, `"state":"finiteWheel"`) {
			t.Fatalf("GET /v1/chatMix: %s", body)
		}
		if code, _ := call("PUT", "/v1/chatMix?balance=0.5", ""); code != http.StatusForbidden {
			t.Errorf("slider with a dial: %d, want 403", code)
		}
		s.HeadsetChatMix(testHeadsetUSB, 40, 100)
		waitEvent("EVENT_SONAR_CHATMIX_DATA", `"balance":0.6`)
		call("PUT", "/classicRedirections/chat/deviceId/test_b", "")
		waitEvent("EVENT_SONAR_CHATMIX_DATA", `"state":"differentDeviceSelected"`)
		if b := s.chatMixBalance(); b != 0 {
			t.Errorf("balance with different devices: %g, want 0", b)
		}
		call("PUT", "/classicRedirections/chat/deviceId/test_a", "")
		waitEvent("EVENT_SONAR_CHATMIX_DATA", `"state":"finiteWheel"`)
	})

	t.Run("fallback", func(t *testing.T) {
		call("PUT", "/fallbackSettings/isEnabled/true", "")
		_, body := call("GET", "/fallbackSettings/lists", "")
		var lists map[string][]fallbackDevice
		json.Unmarshal([]byte(body), &lists)
		if len(lists) != 5 || len(lists["game"]) < 3 {
			t.Fatalf("lists: %s", body)
		}

		destroy("test_a")
		waitFor("Game to fall back to test_b", func() bool { return gameDevice() == "test_b" })
		waitEvent("SONAR_EVENT_REDIRECTION_STATUS_UPDATE", "")
		sink("test_a", `device.vendor.id = "0x1038" device.product.id = "0x22a1"`)
		waitFor("Game back on test_a", func() bool { return gameDevice() == "test_a" })

		call("PUT", "/fallbackSettings/isWirelessTriggerEnabled/true", "")
		heads.setConnected(false)
		waitFor("Game off the disconnected headset", func() bool { return gameDevice() == "test_b" })
		heads.setConnected(true)
		waitFor("Game back on the headset", func() bool { return gameDevice() == "test_a" })

		sink("test_c", "")
		waitFor("Game on the new device", func() bool { return gameDevice() == "test_c" })
		destroy("test_c")
		waitFor("Game back on test_a after test_c", func() bool { return gameDevice() == "test_a" })

		code, body := call("PUT", "/fallbackSettings/excludeDevice/game/test_b", "")
		if code != 200 || !strings.Contains(body, `"id":"test_b","isExcluded":true`) {
			t.Errorf("exclude: %d %s", code, body)
		}
		code, body = call("PUT", "/fallbackSettings/setDevicePosition/game/test_headset_output/test_a", "")
		if i, j := strings.Index(body, `"id":"test_headset_output"`), strings.Index(body, `"id":"test_a"`); code != 200 || i < 0 || i > j {
			t.Errorf("set position: %d %s", code, body)
		}
		call("PUT", "/fallbackSettings/isEnabled/false", "")
	})

	t.Run("samples", func(t *testing.T) {
		code, body := call("PUT", "/audioSamples/game/gaming", "")
		if code != 200 || !strings.Contains(body, `{"role":"game","id":"gaming","isPlaying":true}`) {
			t.Fatalf("play: %d %s", code, body)
		}
		waitFor("the sample on Sonar's Gaming device", func() bool { return linked("sonar_game_sample", "sonar_game") })
		code, body = call("PUT", "/audioSamples/game/gaming", "")
		if code != 200 || !strings.Contains(body, `"id":"gaming","isPlaying":false`) {
			t.Errorf("pause: %d %s", code, body)
		}
		if _, body = call("PUT", "/audioSamples/stop/game", ""); body != `[{"role":"game","id":"gaming","isPlaying":false}]`+"\n" {
			t.Errorf("stop: %q", body)
		}

		if _, body = call("PUT", "/audioSamples/startRecord", ""); !strings.HasPrefix(body, "true") {
			t.Fatalf("startRecord: %s (devices %v)", body, s.redirectionDevices())
		}
		time.Sleep(1500 * time.Millisecond)
		call("PUT", "/audioSamples/stopRecord", "")
		waitEvent("SONAR_EVENT_RECORDING_STOPPED", "")
		if _, body = call("GET", "/audioSamples/samples?role=chatCapture", ""); !strings.Contains(body, `"id":"record"`) {
			t.Fatalf("recording not listed: %s", body)
		}
		if code, body = call("PUT", "/audioSamples/chatCapture/record", ""); code != 200 {
			t.Fatalf("play recording: %d %s", code, body)
		}
		waitFor("the recording in the microphone channel", func() bool { return linked("sonar_mic_sample", "sonar_mic.device") })
		waitFor("the microphone monitored on the chat device", func() bool { return linked("sonar_mic", "test_a") })
		call("PUT", "/audioSamples/stop/chatCapture", "")
		waitFor("the monitor removed", func() bool { return !linked("sonar_mic", "test_a") })
	})
}
