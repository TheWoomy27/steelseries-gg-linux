package sonar

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"ggl/internal/httpx"
)

// Sonar's status, volume, ChatMix and settings endpoints. Settings Sonar
// keeps in its key_value table are served from there; the rest comes from
// the audio engine.

// Roles that have their own volume, fallback list and stream routing.
var channelRoles = []string{"Game", "ChatRender", "ChatCapture", "Media", "Aux"}

type volume struct {
	Volume float64 `json:"volume"`
	Muted  bool    `json:"muted"`
}

type redirectionVolumes struct {
	Stream  map[string]volume `json:"stream"`
	Classic volume            `json:"classic"`
}

func newRedirectionVolumes() redirectionVolumes {
	return redirectionVolumes{
		Stream:  map[string]volume{"streaming": {1, false}, "monitoring": {1, false}},
		Classic: volume{1, false},
	}
}

// volumeInfo is Sonar's VolumeInfo, with the classic and the streamer
// volumes; dictionary keys are camelCased.
func (s *Service) volumeInfo() map[string]any {
	vols := s.classicVolumes()
	stream := s.streamerVolumes()
	devices := map[string]redirectionVolumes{}
	for _, r := range channelRoles {
		v := newRedirectionVolumes()
		v.Classic = vols.Roles[r]
		for _, mix := range streamMixes {
			v.Stream[camel(mix)] = stream.Roles[mix][r]
		}
		devices[camel(r)] = v
	}
	masters := newRedirectionVolumes()
	masters.Classic = vols.Master
	for _, mix := range streamMixes {
		masters.Stream[camel(mix)] = volume{stream.Masters[mix], stream.masterMuted(mix)}
	}
	return map[string]any{"masters": masters, "devices": devices}
}

// setClassicVolume changes a role's volume or mute ("Master" for the master
// volume) and applies it.
func (s *Service) setClassicVolume(role string, set func(*volume)) error {
	vols := s.classicVolumes()
	if role == "Master" {
		set(&vols.Master)
	} else {
		v := vols.Roles[role]
		set(&v)
		vols.Roles[role] = v
	}
	if err := s.saveClassicVolumes(vols); err != nil {
		return err
	}
	if c, ok := channelByRole(role); ok {
		s.applyChannel(c)
	} else {
		s.applyAll()
	}
	return nil
}

// appStatus is Sonar's AppStatus: GET /v1/status and EVENT_SONAR_STATUS.
func (s *Service) appStatus() map[string]any {
	return map[string]any{
		"isEnabled":              s.isActive(),
		"isOnboardingDone":       s.boolSetting("ONBOARDING_FINISHED"),
		"mode":                   camel(s.setting("MODE", modeClassic)),
		"areEndpointsConfigured": s.audio.isRunning(channels[0]),
		"devices":                s.vadStatuses(),
	}
}

// OnStatus sets where Sonar's status goes when its channels change. The
// Windows Sonar sends it over GG's websocket as EVENT_SONAR_STATUS, and
// the client redraws its channels from it (an added Media or Aux channel
// shows up only then).
func (s *Service) OnStatus(fn func(status any)) {
	s.mu.Lock()
	s.onStatus = fn
	s.mu.Unlock()
}

func (s *Service) sendStatus() {
	s.mu.Lock()
	fn := s.onStatus
	s.mu.Unlock()
	if fn != nil {
		fn(s.appStatus())
	}
}

// vadStatuses are Sonar's VadStatus entries for the running channels,
// with the device each plays to or records from and, for a SteelSeries
// device Engine knows, Engine's name for it (QuickSet's tiles compare it).
func (s *Service) vadStatuses() []map[string]any {
	out := []map[string]any{}
	targets := s.redirectionDevices()
	if s.streamMode() {
		targets = s.streamTargets()
	}
	present, usb := map[string]bool{}, map[string]uint32{}
	if objs, err := pwDump(); err == nil {
		for _, d := range devicesIn(objs, "", "") {
			present[d.ID] = true
		}
		usb = usbIDs(objs)
	}
	headsets := s.headsetSource()
	for _, c := range s.enabledChannels() {
		if !s.audio.isRunning(c) {
			continue
		}
		flow := "render"
		if c.capture {
			flow = "capture"
		}
		var cfg any
		if cfgs, err := s.queryConfigs(`WHERE id IN (SELECT config_id FROM selected_config WHERE vad = ?)`, c.vad); err == nil && len(cfgs) > 0 {
			cfg = map[string]string{"id": cfgs[0].ID, "name": cfgs[0].Name}
		}
		var device, name any
		if id := targets[c.redirect]; id != "" && present[id] {
			device = id
			if u, ok := usb[id]; ok && headsets != nil {
				if h, ok := headsets.Headset(u); ok && h.Name != "" {
					name = h.Name
				}
			}
		}
		out = append(out, map[string]any{
			"id": c.node, "dataFlow": flow, "virtualAudioDevice": camel(c.role), "defaultRole": s.defaultRole(c),
			"isChannelEnabled": true, "streamRedirectionDevice": device, "streamRedirectionDeviceName": name, "config": cfg,
		})
	}
	return out
}

// defaultRole reports a Sonar device's Windows default role, as the device
// list does: Gaming is the default output and Microphone the default input.
// PipeWire has no separate communications default, so Chat holds that role
// whenever Sonar's defaults are in place.
func (s *Service) defaultRole(c channel) string {
	devs, err := audioDevices()
	if err != nil {
		return "console"
	}
	for _, d := range devs {
		if d.ID == c.node {
			return d.DefaultRole
		}
	}
	return "console"
}

// camel writes an enum value or dictionary key the way Sonar's API does
// (Newtonsoft with camelCase naming: ChatRender -> chatRender).
func camel(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]|0x20) + s[1:]
}

// boolSetting reads a flag as Sonar does: "True" in any case, missing is
// false.
func (s *Service) boolSetting(key string) bool { return strings.EqualFold(s.setting(key, ""), "true") }

// setBoolSetting writes a flag the way .NET formats a bool.
func (s *Service) setBoolSetting(key string, on bool) error {
	v := "False"
	if on {
		v = "True"
	}
	return s.setSetting(key, v)
}

// boolRoutes serves a GET for a flag and a PUT .../{isEnabled} that
// changes it.
func (s *Service) boolRoutes(path, key string) {
	s.srv.Handle("GET "+path, func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, s.boolSetting(key)) })
	s.srv.Handle("PUT "+path+"/{isEnabled}", func(w http.ResponseWriter, r *http.Request) {
		on, err := parseBool(r.PathValue("isEnabled"))
		if err == nil {
			err = s.setBoolSetting(key, on)
		}
		reply(w, on, err)
	})
}

func (s *Service) statusRoutes() {
	sv := s.srv
	sv.Handle("GET /onboarding", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]bool{"finished": s.boolSetting("ONBOARDING_FINISHED")})
	})
	sv.Handle("GET /v1/status", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, s.appStatus()) })

	sv.Handle("GET /volumeSettings/classic", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, s.volumeInfo()) })
	sv.Handle("GET /volumeSettings/streamer", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, s.volumeInfo()) })
	volumeRoute := func(path string, set func(v *volume, value string) error) {
		sv.Handle("PUT /volumeSettings/classic/"+path, func(w http.ResponseWriter, r *http.Request) {
			role := "Master"
			if p := r.PathValue("role"); p != "" {
				name, ok := enumName(p, roleNames)
				if !ok {
					reply(w, nil, argErrorf("The value '%s' is not valid.", p))
					return
				}
				role = name
			}
			var setErr error
			err := s.setClassicVolume(role, func(v *volume) { setErr = set(v, r.PathValue("value")) })
			if setErr != nil {
				err = setErr
			}
			reply(w, s.volumeInfo(), err)
		})
	}
	setLevel := func(v *volume, value string) error {
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return argErrorf("The value '%s' is not valid.", value)
		}
		v.Volume = math.Max(0, math.Min(1, f))
		return nil
	}
	setMute := func(v *volume, value string) error {
		m, err := parseBool(value)
		v.Muted = m
		return err
	}
	volumeRoute("Master/Volume/{value}", setLevel)
	volumeRoute("Master/Mute/{value}", setMute)
	volumeRoute("{role}/Volume/{value}", setLevel)
	volumeRoute("{role}/Mute/{value}", setMute)

	// Media and Aux are optional channels.
	sv.Handle("PUT /audioInitialization/{role}/isEnabled/{isEnabled}", func(w http.ResponseWriter, r *http.Request) {
		role, ok := enumName(r.PathValue("role"), roleNames)
		on, err := parseBool(r.PathValue("isEnabled"))
		if !ok || err != nil || role != "Media" && role != "Aux" {
			reply(w, nil, argErrorf("Invalid role or value"))
			return
		}
		if err := s.setBoolSetting(channelEnabledKey+role, on); err != nil {
			reply(w, nil, err)
			return
		}
		s.syncAudio()
		w.WriteHeader(http.StatusOK)
	})

	// GameDAC line and headphone outputs.
	sv.Handle("GET /deviceOut", func(w http.ResponseWriter, r *http.Request) {
		out := func(feature, prefix string) map[string]any {
			v, _ := strconv.ParseFloat(s.setting(prefix+"_VOLUME", "0.25"), 64)
			return map[string]any{"volume": v, "mute": s.boolSetting(prefix + "_MUTE"), "feature": feature}
		}
		httpx.JSON(w, map[string]any{
			"selectedDeviceOut": s.setting("DEVICE_OUT_PATH", "HeadphoneOut"),
			"deviceOutSettings": []any{out("HeadphoneOut", "HEADPHONE_OUT"), out("LineOut", "LINE_OUT")},
		})
	})

	sv.Handle("GET /keyboardShortcuts", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, []any{}) })

	// Sonar's in-game overlays (volume, mute, device changes) are not
	// drawn on Linux; their settings page keeps its switches.
	sv.Handle("GET /overlays", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"settings": []any{}, "subAppOverlaysEnabled": s.boolSetting("OVERLAYS_ENABLED")})
	})
	sv.Handle("PUT /overlays/isEnabled/{isEnabled}", func(w http.ResponseWriter, r *http.Request) {
		on, err := parseBool(r.PathValue("isEnabled"))
		if err == nil {
			err = s.setBoolSetting("OVERLAYS_ENABLED", on)
		}
		reply(w, on, err)
	})
	s.boolRoutes("/overlays/isHardwareTriggeredEnabled", "OVERLAYS_HARDWARE_TRIGGERED_ENABLED")
	sv.Handle("PUT /overlays/position/{position}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, r.PathValue("position"), s.setSetting("OVERLAYS_POSITION", r.PathValue("position")))
	})

	// Sonar forwards these to SteelSeries' analytics; gg-linux drops them.
	sv.Handle("POST /analytics/track", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	// In-app toasts come from SteelSeries' online content service.
	sv.Handle("GET /content/inAppToastMessage", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, nil) })
	// Applications playing or recording on each device, for per-app routing.

	sv.Handle("GET /steelseriesDevice/firstDevice", func(w http.ResponseWriter, r *http.Request) {
		devs, err := steelSeriesDevices()
		reply(w, firstSteelSeriesDevice(devs), err)
	})
	sv.Handle("GET /steelseriesDevice/preferredDevice/{deviceName}", func(w http.ResponseWriter, r *http.Request) {
		devs, err := steelSeriesDevices()
		var found *steelSeriesDevice
		for _, d := range devs {
			if d.Name == r.PathValue("deviceName") {
				found = &d
				break
			}
		}
		reply(w, found, err)
	})
	sv.Handle("GET /audioDevices", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		devs, err := audioDevices()
		if err != nil {
			reply(w, nil, err)
			return
		}
		flow := q.Get("deviceDataFlow")
		out := []audioDevice{}
		for _, d := range devs {
			if flow != "" && !strings.EqualFold(flow, "all") && !strings.EqualFold(d.DataFlow, flow) {
				continue
			}
			if q.Get("onlySteelSeriesVAD") == "true" && !d.IsVad || q.Get("removeSteelSeriesVAD") == "true" && d.IsVad {
				continue
			}
			out = append(out, d)
		}
		httpx.JSON(w, out)
	})
}
