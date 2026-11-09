package sonar

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestActiveProfileDevices(t *testing.T) {
	var objs []pwObject
	err := json.Unmarshal([]byte(`[
		{"id": 60, "type": "PipeWire:Interface:Device", "info": {"props": {}, "params": {"Profile": [{"name": "HiFi"}]}}},
		{"id": 61, "type": "PipeWire:Interface:Node", "info": {"state": "suspended", "props": {"node.name": "usb.HiFi__Speaker__sink", "media.class": "Audio/Sink", "device.id": 60, "device.profile.name": "HiFi: Speaker: sink"}}},
		{"id": 62, "type": "PipeWire:Interface:Node", "info": {"state": "suspended", "props": {"node.name": "usb.HiFi_7_1__Speaker__sink", "media.class": "Audio/Sink", "device.id": 60, "device.profile.name": "HiFi 7+1: Speaker: sink"}}},
		{"id": 63, "type": "PipeWire:Interface:Node", "info": {"state": "running", "props": {"node.name": "usb.HiFi_5_1__Speaker__sink", "media.class": "Audio/Sink", "device.id": 60, "device.profile.name": "HiFi 5+1: Speaker: sink"}}},
		{"id": 64, "type": "PipeWire:Interface:Node", "info": {"state": "suspended", "props": {"node.name": "usb.HiFi_7_1__Mic__source", "media.class": "Audio/Source", "device.id": 60, "device.profile.name": "HiFi 7+1: Mic: source"}}},
		{"id": 70, "type": "PipeWire:Interface:Device", "info": {"props": {}, "params": {"Profile": [{"name": "output:analog-stereo+input:mono-fallback"}]}}},
		{"id": 71, "type": "PipeWire:Interface:Node", "info": {"state": "suspended", "props": {"node.name": "headset.analog-stereo", "media.class": "Audio/Sink", "device.id": 70, "device.profile.name": "analog-stereo"}}}
	]`), &objs)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range activeProfileDevices(objs, devicesIn(objs, "", "usb.HiFi_7_1__Mic__source"), "", "usb.HiFi_7_1__Mic__source") {
		got = append(got, d.ID)
	}
	// The active profile's speakers, another profile's running speakers and
	// default microphone, and every node of a card whose profile names its
	// nodes differently.
	want := []string{"usb.HiFi__Speaker__sink", "usb.HiFi_5_1__Speaker__sink", "usb.HiFi_7_1__Mic__source", "headset.analog-stereo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listed %v, want %v", got, want)
	}
}
