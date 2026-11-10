package sonar

import (
	"fmt"
	"strings"
)

// Acoustic echo canceling (Sonar's AEC, the "SonarCiaran" feature):
// sound from speakers that the microphone picks up again is removed from
// the microphone. Windows Sonar offers it when it plays to SteelSeries
// Arena speakers (their spec declares the feature); ggd offers it when
// any device Sonar plays to is speakers rather than a headset or
// headphones, whatever their brand, since speakers on an analog output
// can't say what they are.
//
// PipeWire's echo canceller (WebRTC) sits in Sonar's graph all the time,
// idle until used: turning AEC on or off only relinks it, so no channel
// restarts. When on, the microphone goes into the canceller, the
// canceller's output into the microphone channel, and the monitor of the
// speakers into the canceller as what to remove.

// featureAEC is the Sonar feature that offers echo canceling.
const featureAEC = "SonarCiaran"

const (
	aecSource    = "sonar_aec"           // the microphone without the echo
	aecCapture   = "sonar_aec.capture"   // from the microphone
	aecReference = "sonar_aec.reference" // from the speakers' monitor
)

// aecConf is the echo canceller: mono, at Sonar's rate, with only echo
// canceling on (the microphone channel has its own noise reduction and
// gain). Its output is a stream rather than a source, so it is not
// offered to apps as a microphone.
func aecConf() string {
	return fmt.Sprintf(`  { name = libpipewire-module-echo-cancel
    args = {
      library.name = aec/libspa-aec-webrtc
      monitor.mode = true
      audio.rate = %d
      audio.channels = 1
      audio.position = [ MONO ]
      aec.args = { webrtc.noise_suppression = false webrtc.gain_control = false webrtc.high_pass_filter = false webrtc.transient_suppression = false webrtc.voice_detection = false }
      capture.props = { node.name = %q node.description = "SteelSeries Sonar - Echo canceling" node.autoconnect = false }
      source.props = { node.name = %q node.description = "SteelSeries Sonar - Echo canceling" media.class = Stream/Output/Audio node.autoconnect = false }
      sink.props = { node.name = %q node.description = "SteelSeries Sonar - Echo canceling" node.autoconnect = false node.passive = true }
    }
  }
`, sampleRate, aecCapture, aecSource, aecReference)
}

// isSpeakers reports whether an output device (a sink's properties) is
// speakers: not a headset, headphones or a handset, by its form factor
// or, where it has none, its name and port.
func isSpeakers(props map[string]any) bool {
	str := func(k string) string { s, _ := props[k].(string); return strings.ToLower(s) }
	switch str("device.form-factor") {
	case "headset", "headphone", "hands-free", "handset":
		return false
	case "speaker", "internal", "computer", "tv", "car", "hifi":
		return true
	}
	if str("media.class") != "audio/sink" {
		return false
	}
	for _, k := range []string{"node.name", "node.description", "device.profile.name", "device.profile.description"} {
		v := str(k)
		if strings.Contains(v, "headphone") || strings.Contains(v, "headset") {
			return false
		}
	}
	return true
}

// aecRedirections are the redirections whose devices can be the speakers
// to cancel, in order of preference.
var aecRedirections = []string{"Game", "Chat", "Media", "Aux", "Monitoring"}

// aecSpeakers is the speakers to cancel among the devices Sonar plays to
// (targets, device node.name by redirection), "" for none.
func aecSpeakers(targets map[string]string, objs []pwObject) string {
	speakers := map[string]bool{}
	for _, o := range objs {
		if o.Type != "PipeWire:Interface:Node" {
			continue
		}
		if n, _ := o.Info.Props["node.name"].(string); n != "" && !strings.HasPrefix(n, "sonar_") && isSpeakers(o.Info.Props) {
			speakers[n] = true
		}
	}
	for _, r := range aecRedirections {
		if t := targets[r]; t != "" && speakers[t] {
			return t
		}
	}
	return ""
}

// aecAvailable reports whether echo canceling is offered: Sonar plays to
// speakers.
func (s *Service) aecAvailable() bool {
	targets := s.redirectionDevices()
	if s.streamMode() {
		targets = s.streamTargets()
	}
	objs, err := pwDump()
	return err == nil && aecSpeakers(targets, objs) != ""
}
