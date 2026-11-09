package sonar

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStreamModeOnPipeWire runs streamer mode on a private PipeWire
// instance with WirePlumber (see TestRoutingOnPipeWire), measuring what
// reaches the monitoring and streaming devices.
func TestStreamModeOnPipeWire(t *testing.T) {
	p := newPWTest(t)
	call := p.call
	tone := filepath.Join(p.tmp, "tone.wav")
	writeTone(t, tone, 1500*time.Millisecond)
	// level plays the tone into a node (a sink, or the test microphone)
	// and returns the level a device records.
	level := func(device, into string) float64 {
		t.Helper()
		return p.level(device, into, tone)
	}
	near, silent := p.near, p.silent

	classic := level("test_head", "sonar_game")
	t.Logf("classic Game on the headset: %.1f dBFS", classic)

	call("PUT", "/mode/stream", "")
	body := call("GET", "/streamRedirections", "")
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
	if body = call("GET", "/streamRedirections/isStreamMonitoringLocked", ""); !strings.HasPrefix(body, "true") {
		t.Errorf("monitoring the streaming device should lock stream monitoring: %s", body)
	}
	call("PUT", "/streamRedirections/monitoring/deviceId/test_head", "")

	call("PUT", "/mode/classic", "")
	near("back in classic mode", level("test_head", "sonar_game"), classic)
}
