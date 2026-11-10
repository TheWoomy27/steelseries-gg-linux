package sonar

import "testing"

func TestIsSpeakers(t *testing.T) {
	for _, c := range []struct {
		props map[string]any
		want  bool
	}{
		// The user's devices as PipeWire describes them.
		{map[string]any{"media.class": "Audio/Sink", "node.name": "alsa_output.usb-SteelSeries_Arctis_Nova_7-00.analog-stereo", "device.form-factor": "headset"}, false},
		{map[string]any{"media.class": "Audio/Sink", "node.name": "alsa_output.usb-Generic_USB_Audio-00.HiFi__Speaker__sink", "node.description": "USB Audio Speakers"}, true},
		{map[string]any{"media.class": "Audio/Sink", "node.name": "alsa_output.usb-Generic_USB_Audio-00.HiFi__Headphones__sink", "node.description": "USB Audio Front Headphones"}, false},
		{map[string]any{"media.class": "Audio/Sink", "node.name": "alsa_output.usb-Generic_USB_Audio-00.HiFi__SPDIF__sink", "node.description": "USB Audio S/PDIF Output"}, true},
		{map[string]any{"media.class": "Audio/Sink", "node.name": "bluez_output.xx", "device.form-factor": "headphone"}, false},
		{map[string]any{"media.class": "Audio/Sink", "node.name": "alsa_output.pci.hdmi-stereo", "device.form-factor": "internal"}, true},
		{map[string]any{"media.class": "Audio/Source", "node.name": "alsa_input.mic"}, false},
	} {
		if got := isSpeakers(c.props); got != c.want {
			t.Errorf("%v: %v, want %v", c.props["node.name"], got, c.want)
		}
	}
}

func TestAECSpeakers(t *testing.T) {
	node := func(name string, props map[string]any) pwObject {
		var o pwObject
		o.Type = "PipeWire:Interface:Node"
		o.Info.Props = map[string]any{"node.name": name, "media.class": "Audio/Sink"}
		for k, v := range props {
			o.Info.Props[k] = v
		}
		return o
	}
	objs := []pwObject{
		node("headset", map[string]any{"device.form-factor": "headset"}),
		node("speakers", nil),
		node("sonar_game", nil),
	}
	if got := aecSpeakers(map[string]string{"Game": "headset", "Chat": "headset"}, objs); got != "" {
		t.Errorf("a headset only: %q", got)
	}
	if got := aecSpeakers(map[string]string{"Game": "headset", "Media": "speakers"}, objs); got != "speakers" {
		t.Errorf("speakers on Media: %q", got)
	}
	if got := aecSpeakers(map[string]string{"Game": "missing"}, objs); got != "" {
		t.Errorf("a missing device: %q", got)
	}
}
