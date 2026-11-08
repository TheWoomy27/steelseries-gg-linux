package sonar

import (
	"net/http"
	"strconv"
	"strings"

	"ggl/internal/httpx"
)

// Endpoints the Sonar pages read on load. Settings Sonar keeps in its
// key_value table are served from there; audio state (volumes, ChatMix,
// device status) is neutral until the PipeWire side drives it.

// Roles that have their own volume, fallback list and stream routing.
var channelRoles = []string{"Game", "ChatRender", "ChatCapture", "Media", "Aux"}

// fallbackKeys are the key_value entries holding each role's fallback
// devices.
var fallbackKeys = map[string]string{
	"Game": "GAME_FALLBACK_DEVICES", "ChatRender": "CHAT_RENDER_FALLBACK_DEVICES",
	"ChatCapture": "CHAT_CAPTURE_FALLBACK_DEVICES", "Media": "MEDIA_FALLBACK_DEVICES", "Aux": "AUX_FALLBACK_DEVICES",
}

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

// volumeInfo is Sonar's VolumeInfo; dictionary keys are camelCased.
func volumeInfo() map[string]any {
	devices := map[string]redirectionVolumes{}
	for _, r := range channelRoles {
		devices[lowerFirst(r)] = newRedirectionVolumes()
	}
	return map[string]any{"masters": newRedirectionVolumes(), "devices": devices}
}

func lowerFirst(s string) string {
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
	sv.Handle("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{
			"isEnabled":              true,
			"isOnboardingDone":       s.boolSetting("ONBOARDING_FINISHED"),
			"mode":                   s.setting("MODE", modeClassic),
			"areEndpointsConfigured": false,
			"devices":                []any{},
		})
	})
	sv.Handle("GET /v1/chatMix", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"balance": 0.0, "state": "Enabled", "id": "00000000-0000-0000-0000-000000000000"})
	})

	sv.Handle("GET /volumeSettings/classic", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, volumeInfo()) })
	sv.Handle("GET /volumeSettings/streamer", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, volumeInfo()) })
	sv.Handle("GET /volumeSettings/devices/volumes", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, []any{}) })

	sv.Handle("GET /streamRedirections/isStreamMonitoringLocked", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, false) })
	s.boolRoutes("/streamRedirections/isStreamMonitoringEnabled", "STREAM_MODE_IS_STREAM_MONITORING_ENABLED")

	s.boolRoutes("/fallbackSettings/isEnabled", "IS_FALLBACK_ENABLED")
	s.boolRoutes("/fallbackSettings/isWirelessTriggerEnabled", "IS_STEELSERIES_WIRELESS_TRIGGER_ENABLED")
	s.boolRoutes("/fallbackSettings/isFallbackOnRebootEnabled", "IS_FALLBACK_ON_REBOOT_ENABLED")
	sv.Handle("GET /fallbackSettings/lists", func(w http.ResponseWriter, r *http.Request) {
		lists := []map[string]any{}
		for _, role := range channelRoles {
			lists = append(lists, map[string]any{"key": role, "value": []any{}})
		}
		httpx.JSON(w, lists)
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

	sv.Handle("GET /linkAll/isRenderLinkAllEnabled", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, false) })
	sv.Handle("GET /keyboardShortcuts", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, []any{}) })

	// QuickSet profiles and in-game overlays come from GG features that
	// are not available on Linux.
	sv.Handle("GET /quickset/isEnabled", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, false) })
	sv.Handle("GET /quickset/selected", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, nil) })
	sv.Handle("GET /quickset/profiles", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, []any{}) })
	sv.Handle("GET /overlays", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"settings": []any{}, "subAppOverlaysEnabled": false})
	})
	sv.Handle("GET /overlays/isHardwareTriggeredEnabled", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, false) })

	sv.Handle("GET /audioSamples/isRecording", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, false) })
	sv.Handle("PUT /audioSamples/stopRecord", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

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
			if flow != "" && flow != "All" && d.DataFlow != flow {
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
