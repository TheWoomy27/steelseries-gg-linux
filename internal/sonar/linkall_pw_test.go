package sonar

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestLinkAllOnPipeWire links the output channels to Game's device on a
// private PipeWire instance (see newPWTest).
func TestLinkAllOnPipeWire(t *testing.T) {
	p := newPWTest(t)
	if out, err := pwCommand("pw-cli", "create-node", "adapter",
		`{ factory.name = support.null-audio-sink node.name = test_speakers media.class = Audio/Sink audio.position = [ FL FR ] object.linger = true }`).CombinedOutput(); err != nil {
		t.Fatalf("pw-cli: %v: %s", err, out)
	}
	t.Cleanup(func() { pwCommand("pw-cli", "destroy", "test_speakers").Run() })
	time.Sleep(1500 * time.Millisecond) // the device watch lists it
	p.call("PUT", "/classicRedirections/media/deviceId/test_speakers", "")
	p.call("PUT", "/fallbackSettings/excludeDevice/game/test_speakers", "")
	// Sonar starts new setups linked.
	if got := p.call("GET", "/linkAll/isRenderLinkAllEnabled", ""); got != "true\n" && got != "true" {
		t.Errorf("Link All starts off: %q", got)
	}
	p.call("PUT", "/linkAll/unlink", "")
	if got := p.call("GET", "/linkAll/isRenderLinkAllEnabled", ""); got != "false\n" && got != "false" {
		t.Fatalf("Link All is %q after unlinking", got)
	}

	var data linkAllData
	if err := json.Unmarshal([]byte(p.call("PUT", "/linkAll/link/game", "")), &data); err != nil {
		t.Fatal(err)
	}
	if !data.IsEnabled {
		t.Errorf("link returned isEnabled false")
	}
	devices := map[string]string{}
	for _, r := range data.ClassicRedirections {
		if r.DeviceID != nil {
			devices[r.ID] = *r.DeviceID
		}
	}
	for _, id := range []string{"game", "chat", "media", "aux"} {
		if devices[id] != "test_head" {
			t.Errorf("%s plays to %q after linking, want test_head", id, devices[id])
		}
	}
	if devices["mic"] != "test_headset_mic" {
		t.Errorf("linking moved the microphone to %q", devices["mic"])
	}
	ids := func(list []fallbackDevice) (out []string) {
		for _, d := range list {
			if !d.IsExcluded {
				out = append(out, d.ID)
			}
		}
		return out
	}
	for _, role := range []string{"chatRender", "media", "aux"} {
		if !reflect.DeepEqual(ids(data.Devices[role]), ids(data.Devices["game"])) {
			t.Errorf("%s's priority list %v, want Game's %v", role, ids(data.Devices[role]), ids(data.Devices["game"]))
		}
	}
	if got := p.call("GET", "/linkAll/isRenderLinkAllEnabled", ""); got != "true\n" && got != "true" {
		t.Errorf("Link All is %q after linking", got)
	}
}
