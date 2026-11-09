package sonar

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

const routingDump = `[
 {"id": 30, "type": "PipeWire:Interface:Node", "info": {"state": "running", "props": {"node.name": "alsa_output.speakers", "media.class": "Audio/Sink"}, "params": {"Props": [{"volume": 1.0}, {"channelVolumes": [0.125, 0.125], "mute": false}]}}},
 {"id": 31, "type": "PipeWire:Interface:Node", "info": {"state": "running", "props": {"node.name": "sonar_game", "media.class": "Audio/Sink"}}},
 {"id": 32, "type": "PipeWire:Interface:Node", "info": {"state": "running", "props": {"node.name": "sonar_game.device", "media.class": "Stream/Output/Audio"}}},
 {"id": 33, "type": "PipeWire:Interface:Node", "info": {"state": "running", "props": {"node.name": "alsa_input.mic", "media.class": "Audio/Source"}}},
 {"id": 40, "type": "PipeWire:Interface:Node", "info": {"state": "running", "props": {"media.class": "Stream/Output/Audio", "application.name": "Firefox", "application.process.binary": "firefox", "application.process.id": "1234"}}},
 {"id": 41, "type": "PipeWire:Interface:Node", "info": {"state": "idle", "props": {"media.class": "Stream/Input/Audio", "application.name": "Discord", "application.process.binary": "Discord", "application.process.id": 2345}}},
 {"id": 42, "type": "PipeWire:Interface:Node", "info": {"state": "running", "props": {"media.class": "Stream/Input/Audio", "application.name": "visualizer"}}},
 {"id": 43, "type": "PipeWire:Interface:Node", "info": {"state": "running", "props": {"media.class": "Stream/Output/Audio", "application.name": "libcanberra", "media.role": "event"}}},
 {"id": 44, "type": "PipeWire:Interface:Node", "info": {"state": "running", "props": {"media.class": "Stream/Output/Audio", "application.name": "unlinked", "application.process.id": "99"}}},
 {"id": 45, "type": "PipeWire:Interface:Node", "info": {"state": "running", "props": {"node.name": "gg_audio_meter", "media.class": "Stream/Input/Audio", "application.name": "pw-record", "application.process.id": 3456, "stream.monitor": true}}},
 {"id": 50, "type": "PipeWire:Interface:Link", "info": {"output-node-id": 40, "input-node-id": 31}},
 {"id": 51, "type": "PipeWire:Interface:Link", "info": {"output-node-id": 32, "input-node-id": 30}},
 {"id": 52, "type": "PipeWire:Interface:Link", "info": {"output-node-id": 33, "input-node-id": 41}},
 {"id": 53, "type": "PipeWire:Interface:Link", "info": {"output-node-id": 30, "input-node-id": 42}},
 {"id": 54, "type": "PipeWire:Interface:Link", "info": {"output-node-id": 43, "input-node-id": 30}},
 {"id": 55, "type": "PipeWire:Interface:Link", "info": {"output-node-id": 33, "input-node-id": 45}}
]`

func TestAppStreams(t *testing.T) {
	var objs []pwObject
	if err := json.Unmarshal([]byte(routingDump), &objs); err != nil {
		t.Fatal(err)
	}
	got := sessionsByDevice(devicesIn(objs, "sonar_game", "alsa_input.mic"), appStreams(objs))
	want := []deviceAudioSessions{
		{"alsa_output.speakers", "none", "render", []audioSession{
			{ID: "43", ProcessName: "libcanberra", ProcessID: syntheticPID + 43, IsSystemSound: true, State: "active", DisplayName: "libcanberra"},
		}},
		{"sonar_game", "game", "render", []audioSession{
			{ID: "40", ProcessName: "firefox", ProcessID: 1234, State: "active", DisplayName: "Firefox"},
		}},
		{"alsa_input.mic", "none", "capture", []audioSession{
			{ID: "41", ProcessName: "Discord", ProcessID: 2345, State: "inactive", DisplayName: "Discord"},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		t.Errorf("sessions:\n%s", g)
	}
}

func TestNodeVolumes(t *testing.T) {
	var objs []pwObject
	if err := json.Unmarshal([]byte(routingDump), &objs); err != nil {
		t.Fatal(err)
	}
	got := nodeVolumes(objs)
	if len(got) != 1 || math.Abs(got["alsa_output.speakers"]-0.5) > 1e-9 {
		t.Errorf("volumes %v, want the speakers at 0.5", got)
	}
}
