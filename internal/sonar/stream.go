package sonar

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"

	"ggl/internal/httpx"
)

// Streamer mode. Each playback channel plays to two mixes with their own
// volumes: the monitoring mix, on the user's headset, and the streaming
// mix, on the streaming device (by default nothing; the Stream device is
// what streaming software records). A copy of the processed microphone can
// join either mix, and the microphone channel records from its own device.
// With stream monitoring the user hears the streaming device instead of a
// mix of their own. This follows Sonar's stream redirections and
// SonarDeviceVolumeService.

var (
	streamMixes = []string{"Streaming", "Monitoring"}
	// The roles a mix's master volume and mute cover.
	masterRoles = []string{"Game", "ChatRender", "Media", "Aux"}
)

const streamerVolumesKey = "LINUX_STREAMER_VOLUMES"

// streamerVolumes are each mix's volumes, by role, and master volume. A
// master mute is not stored: a mix is muted while all its roles are.
type streamerVolumes struct {
	Masters map[string]float64           `json:"masters"`
	Roles   map[string]map[string]volume `json:"roles"`
}

func (s *Service) streamerVolumes() streamerVolumes {
	v := streamerVolumes{}
	json.Unmarshal([]byte(s.setting(streamerVolumesKey, "{}")), &v)
	if v.Masters == nil {
		v.Masters = map[string]float64{}
	}
	if v.Roles == nil {
		v.Roles = map[string]map[string]volume{}
	}
	for _, mix := range streamMixes {
		if v.Roles[mix] == nil {
			v.Roles[mix] = map[string]volume{}
		}
		for _, r := range channelRoles {
			if _, ok := v.Roles[mix][r]; !ok {
				v.Roles[mix][r] = volume{1, false}
			}
		}
		if _, ok := v.Masters[mix]; !ok {
			for _, r := range masterRoles {
				v.Masters[mix] = math.Max(v.Masters[mix], v.Roles[mix][r].Volume)
			}
		}
	}
	return v
}

func (v streamerVolumes) masterMuted(mix string) bool {
	for _, r := range masterRoles {
		if !v.Roles[mix][r].Muted {
			return false
		}
	}
	return true
}

func (s *Service) streamMode() bool { return s.setting("MODE", modeClassic) == modeStream }

func (s *Service) streamDevice(id string) string {
	return s.setting(upperSnake(id)+"_STREAM_MODE_DEVICE_ID", "")
}

func (s *Service) streamEnabled(mix, role string) bool {
	return s.boolSetting(upperSnake(mix) + "_STREAM_MODE_" + upperSnake(role) + "_IS_ENABLED")
}

// streamGain is a role's gain in a mix: its volume, used as a linear gain
// as Sonar does, unless it is muted or left out of the mix.
func (s *Service) streamGain(v streamerVolumes, mix, role string) float64 {
	rv := v.Roles[mix][role]
	if rv.Muted || !s.streamEnabled(mix, role) {
		return 0
	}
	return rv.Volume
}

// streamMonitoringLocked: listening to the stream needs a monitoring device
// other than the streaming one.
func (s *Service) streamMonitoringLocked() bool {
	mon := s.streamDevice("Monitoring")
	return mon == "" || mon == s.streamDevice("Streaming")
}

func (s *Service) listening() bool {
	return s.boolSetting("STREAM_MODE_IS_STREAM_MONITORING_ENABLED") && !s.streamMonitoringLocked()
}

// streamTargets are the devices of streamer mode's graph (see relink).
func (s *Service) streamTargets() map[string]string {
	mon := s.streamDevice("Monitoring")
	t := map[string]string{"Mic": s.streamDevice("Mic"), "Monitoring": mon, "Streaming": s.streamDevice("Streaming"), "Chat": mon}
	if s.listening() {
		t["Listen"], t["Monitoring"] = mon, ""
		mon = ""
	}
	for _, id := range renderRedirectionIDs {
		t[id] = mon
	}
	return t
}

// enterStreamMode starts streamer mode on the classic devices the first
// time, so the user hears their mix before choosing devices (Windows
// leaves them empty until chosen).
func (s *Service) enterStreamMode() {
	devs := s.redirectionDevices()
	if s.streamDevice("Monitoring") == "" && devs["Game"] != "" {
		s.setSetting("MONITORING_STREAM_MODE_DEVICE_ID", devs["Game"])
	}
	if s.streamDevice("Mic") == "" && devs["Mic"] != "" {
		s.setSetting("MIC_STREAM_MODE_DEVICE_ID", devs["Mic"])
	}
}

// applyMicCopy sets the microphone copy's gain in each mix.
func (s *Service) applyMicCopy() {
	v := s.streamerVolumes()
	s.audio.setParam(micCopy, podParams([]any{
		"mon:Mult", s.streamGain(v, "Monitoring", "ChatCapture"),
		"str:Mult", s.streamGain(v, "Streaming", "ChatCapture"),
	}))
}

// applyStreamRouting follows changes of streamer mode's devices and mixes.
func (s *Service) applyStreamRouting() {
	if !s.streamMode() {
		return
	}
	s.audio.setTargets(s.streamTargets())
	s.applyAll()
	s.applyMicCopy()
}

func (s *Service) setStreamerVolumes(v streamerVolumes) error {
	if err := s.setSetting(streamerVolumesKey, jsonText(v)); err != nil {
		return err
	}
	s.applyAll()
	s.applyMicCopy()
	return nil
}

func (s *Service) streamerRoutes() {
	sv := s.srv
	// mixArgs reads the mix, and the role unless master, of a streamer
	// volume route.
	mixArgs := func(r *http.Request, master bool) (string, string, error) {
		if !s.streamMode() {
			return "", "", fmt.Errorf("streamer volumes need streamer mode")
		}
		mix, ok := enumName(r.PathValue("id"), streamRedirectionNames)
		if !ok || !slices.Contains(streamMixes, mix) {
			return "", "", argErrorf("The value '%s' is not valid.", r.PathValue("id"))
		}
		if master {
			return mix, "", nil
		}
		role, ok := enumName(r.PathValue("role"), roleNames)
		if !ok || !slices.Contains(channelRoles, role) {
			return "", "", argErrorf("The value '%s' is not valid.", r.PathValue("role"))
		}
		return mix, role, nil
	}
	clamp := func(v string) (float64, error) {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, argErrorf("The value '%s' is not valid.", v)
		}
		return math.Max(0, math.Min(1, f)), nil
	}
	sv.Handle("PUT /volumeSettings/streamer/{id}/master/volume/{value}", func(w http.ResponseWriter, r *http.Request) {
		mix, _, err := mixArgs(r, true)
		var vol float64
		if err == nil {
			vol, err = clamp(r.PathValue("value"))
		}
		if err != nil {
			reply(w, nil, err)
			return
		}
		// The master scales the roles' volumes.
		v := s.streamerVolumes()
		old := v.Masters[mix]
		for _, role := range masterRoles {
			rv := v.Roles[mix][role]
			ratio := 1.0
			if !almostEqual(old, 0) {
				ratio = rv.Volume / old
			}
			rv.Volume = vol * ratio
			v.Roles[mix][role] = rv
		}
		v.Masters[mix] = vol
		reply(w, s.volumeInfo(), s.setStreamerVolumes(v))
	})
	sv.Handle("PUT /volumeSettings/streamer/{id}/master/isMuted/{value}", func(w http.ResponseWriter, r *http.Request) {
		mix, _, err := mixArgs(r, true)
		var mute bool
		if err == nil {
			mute, err = parseBool(r.PathValue("value"))
		}
		if err != nil {
			reply(w, nil, err)
			return
		}
		v := s.streamerVolumes()
		for _, role := range masterRoles {
			rv := v.Roles[mix][role]
			rv.Muted = mute
			v.Roles[mix][role] = rv
		}
		reply(w, s.volumeInfo(), s.setStreamerVolumes(v))
	})
	sv.Handle("PUT /volumeSettings/streamer/{id}/{role}/volume/{value}", func(w http.ResponseWriter, r *http.Request) {
		mix, role, err := mixArgs(r, false)
		var vol float64
		if err == nil {
			vol, err = clamp(r.PathValue("value"))
		}
		if err != nil {
			reply(w, nil, err)
			return
		}
		v := s.streamerVolumes()
		rv := v.Roles[mix][role]
		rv.Volume = vol
		v.Roles[mix][role] = rv
		// A role above the master raises it.
		if role != "ChatCapture" && vol > v.Masters[mix] {
			v.Masters[mix] = vol
		}
		reply(w, s.volumeInfo(), s.setStreamerVolumes(v))
	})
	sv.Handle("PUT /volumeSettings/streamer/{id}/{role}/isMuted/{value}", func(w http.ResponseWriter, r *http.Request) {
		mix, role, err := mixArgs(r, false)
		var mute bool
		if err == nil {
			mute, err = parseBool(r.PathValue("value"))
		}
		if err != nil {
			reply(w, nil, err)
			return
		}
		v := s.streamerVolumes()
		rv := v.Roles[mix][role]
		rv.Muted = mute
		v.Roles[mix][role] = rv
		reply(w, s.volumeInfo(), s.setStreamerVolumes(v))
	})
	sv.Handle("GET /streamRedirections/isStreamMonitoringLocked", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, s.streamMonitoringLocked())
	})
	sv.Handle("GET /streamRedirections/isStreamMonitoringEnabled", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, s.listening())
	})
	sv.Handle("PUT /streamRedirections/isStreamMonitoringEnabled/{isEnabled}", func(w http.ResponseWriter, r *http.Request) {
		on, err := parseBool(r.PathValue("isEnabled"))
		if err == nil {
			err = s.setBoolSetting("STREAM_MODE_IS_STREAM_MONITORING_ENABLED", on && !s.streamMonitoringLocked())
		}
		if err == nil {
			s.applyStreamRouting()
		}
		reply(w, s.listening(), err)
	})
}

// streamRoutingChanged follows a change of a stream redirection: the
// devices, and whether listening to the stream is possible.
func (s *Service) streamRoutingChanged(lockedBefore bool) {
	if locked := s.streamMonitoringLocked(); locked != lockedBefore {
		if locked {
			s.setBoolSetting("STREAM_MODE_IS_STREAM_MONITORING_ENABLED", false)
		}
		s.ws.Broadcast("SONAR_EVENT_STREAM_MONITORING_LOCK_STATUS_UPDATE", locked)
	}
	s.applyStreamRouting()
	s.ws.Broadcast("SONAR_EVENT_REDIRECTION_STATUS_UPDATE", nil)
}
