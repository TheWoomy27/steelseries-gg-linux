package sonar

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestRoutingOnPipeWire moves a playing app between outputs through
// Sonar's API on a private PipeWire instance with WirePlumber, which
// testdata/pipewire.sh starts. Point GG_TEST_PIPEWIRE at that instance's
// runtime directory and GG_LINUX_DATA at data imported by gg-setup (for
// Sonar's database migrations).
func TestRoutingOnPipeWire(t *testing.T) {
	runtime, data := os.Getenv("GG_TEST_PIPEWIRE"), os.Getenv("GG_LINUX_DATA")
	if runtime == "" || data == "" {
		t.Skip("GG_TEST_PIPEWIRE or GG_LINUX_DATA not set")
	}
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "sonar"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("cp", "-r", filepath.Join(data, "sonar", "db-migrations"), filepath.Join(tmp, "sonar")).Run(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GG_LINUX_DATA", tmp)
	t.Setenv("PIPEWIRE_RUNTIME_DIR", runtime)

	// A second output to move the app to.
	out, err := pwCommand("pw-cli", "create-node", "adapter",
		`{ factory.name = support.null-audio-sink node.name = test_speakers media.class = Audio/Sink audio.position = [ FL FR ] object.linger = true }`).CombinedOutput()
	if err != nil {
		t.Fatalf("pw-cli: %v: %s", err, out)
	}
	t.Cleanup(func() { pwCommand("pw-cli", "destroy", "test_speakers").Run() })

	s, err := New("test")
	if err != nil {
		t.Fatal(err)
	}
	s.SetActive(true)
	defer s.SetActive(false)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/sock", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	events := make(chan string, 64)
	go func() {
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				close(events)
				return
			}
			events <- string(msg)
		}
	}()

	tone := filepath.Join(tmp, "tone.wav")
	writeTone(t, tone, 8*time.Second)
	// A new app name each run: WirePlumber remembers where apps were moved.
	app := fmt.Sprintf("Routing Test %d", time.Now().UnixNano())
	player := pwCommand("pw-cat", "-p", "-P", fmt.Sprintf("{ application.name = %q }", app), tone)
	if err := player.Start(); err != nil {
		t.Fatal(err)
	}
	defer player.Process.Kill()
	pid := player.Process.Pid

	call := func(method, path string) (*http.Response, []byte) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer test")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return resp, []byte(b.String())
	}
	deviceOf := func() string {
		_, body := call("GET", "/AudioDeviceRouting")
		var list []deviceAudioSessions
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatalf("GET /AudioDeviceRouting: %v: %s", err, body)
		}
		for _, d := range list {
			for _, s := range d.AudioSessions {
				if s.ProcessID == pid && s.State == "active" {
					return d.DeviceID
				}
			}
		}
		return ""
	}
	waitFor := func(what string, ok func() bool) {
		for deadline := time.Now().Add(4 * time.Second); !ok(); time.Sleep(200 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	waitEvent := func(name, contains string) {
		timeout := time.After(4 * time.Second)
		for {
			select {
			case e, ok := <-events:
				if !ok {
					t.Fatalf("websocket closed waiting for %s", name)
				}
				if strings.Contains(e, `"event":"`+name+`"`) && strings.Contains(e, contains) {
					t.Logf("%s", e)
					return
				}
			case <-timeout:
				t.Fatalf("no %s event containing %s", name, contains)
			}
		}
	}

	waitFor("the player on the default output", func() bool { return deviceOf() == "test_headset_output" })
	waitEvent("SONAR_EVENT_AUDIO_SESSION_OPENED_DATA", `"processId":`+strconv.Itoa(pid))

	resp, body := call("PUT", fmt.Sprintf("/AudioDeviceRouting/render/test_speakers/%d", pid))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT: %d %s", resp.StatusCode, body)
	}
	waitFor("the player on test_speakers", func() bool { return deviceOf() == "test_speakers" })
	waitEvent("SONAR_EVENT_ROUTING_DATA", `"deviceId":"test_speakers"`)

	resp, _ = call("PUT", fmt.Sprintf("/AudioDeviceRouting/sideways/test_speakers/%d", pid))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad data flow: %d, want 400", resp.StatusCode)
	}

	player.Process.Kill()
	player.Wait()
	waitEvent("SONAR_EVENT_AUDIO_SESSION_CLOSED_DATA", `"processId":`+strconv.Itoa(pid))
}
