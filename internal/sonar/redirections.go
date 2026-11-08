package sonar

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"ggl/internal/httpx"
)

// Classic mode routing: the output device each playback channel plays to and
// the microphone the Mic channel captures from. Windows keeps these in its
// virtual audio driver; here they are a key_value entry that the PipeWire
// graph follows.

const redirectionsKey = "LINUX_CLASSIC_REDIRECTIONS"

// Sonar's RedirectionId values, by enum number.
var redirectionNames = map[int]string{1: "Game", 2: "Chat", 3: "Mic", 4: "MicPlayer", 5: "MicCopy", 6: "Media", 7: "Aux"}

var (
	classicRedirectionIDs = []string{"Game", "Chat", "Mic", "Media", "Aux"}
	renderRedirectionIDs  = []string{"Game", "Chat", "Media", "Aux"}
)

// roleRedirections maps device roles to the redirection that serves them.
var roleRedirections = map[string]string{
	"Game": "Game", "ChatRender": "Chat", "ChatCapture": "Mic", "Media": "Media", "Aux": "Aux",
}

// enumName resolves an enum route or JSON value the way ASP.NET binds it:
// a name in any case, or its number.
func enumName(v string, names map[int]string) (string, bool) {
	if n, err := strconv.Atoi(v); err == nil {
		name, ok := names[n]
		return name, ok
	}
	for _, name := range names {
		if strings.EqualFold(name, v) {
			return name, true
		}
	}
	return "", false
}

var roleNames = map[int]string{0: "None", 1: "ChatRender", 2: "Game", 3: "ChatCapture", 4: "Stream", 5: "Media", 6: "Aux"}

func (s *Service) redirectionDevices() map[string]string {
	devs := map[string]string{}
	json.Unmarshal([]byte(s.setting(redirectionsKey, "{}")), &devs)
	return devs
}

func (s *Service) setRedirectionDevice(id, deviceID string) error {
	devs := s.redirectionDevices()
	if deviceID == "" {
		delete(devs, id)
	} else {
		devs[id] = deviceID
	}
	return s.setSetting(redirectionsKey, jsonText(devs))
}

type classicRedirection struct {
	ID        string  `json:"id"`
	DeviceID  *string `json:"deviceId"`
	IsRunning bool    `json:"isRunning"`
}

func (s *Service) classicRedirection(id string) classicRedirection {
	r := classicRedirection{ID: camel(id)}
	if d, ok := s.redirectionDevices()[id]; ok {
		r.DeviceID = &d
	}
	return r
}

func (s *Service) classicRedirections(ids []string) []classicRedirection {
	out := []classicRedirection{}
	for _, id := range ids {
		out = append(out, s.classicRedirection(id))
	}
	return out
}

// Streamer mode routing: the Streaming and Monitoring mixes and the
// streamed microphone, each with an output device and the roles it carries.
// Sonar keeps these in key_value.

var (
	streamRedirectionNames = map[int]string{0: "Streaming", 1: "Monitoring", 2: "Mic"}
	streamRedirectionIDs   = []string{"Streaming", "Monitoring", "Mic"}
	streamRoles            = []string{"ChatCapture", "ChatRender", "Game", "Media", "Aux"}
)

// upperSnake converts Sonar's enum names the way its KeyConverter does
// (ChatCapture -> CHAT_CAPTURE).
func upperSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

type streamRedirection struct {
	StreamRedirectionID string         `json:"streamRedirectionId"`
	DeviceID            *string        `json:"deviceId"`
	Status              []streamStatus `json:"status"`
	IsRunning           bool           `json:"isRunning"`
}

type streamStatus struct {
	Role      string `json:"role"`
	IsEnabled bool   `json:"isEnabled"`
}

func (s *Service) streamRedirection(id string) streamRedirection {
	r := streamRedirection{StreamRedirectionID: camel(id), Status: []streamStatus{}}
	if d := s.setting(upperSnake(id)+"_STREAM_MODE_DEVICE_ID", ""); d != "" {
		r.DeviceID = &d
	}
	for _, role := range streamRoles {
		r.Status = append(r.Status, streamStatus{camel(role), s.boolSetting(upperSnake(id) + "_STREAM_MODE_" + upperSnake(role) + "_IS_ENABLED")})
	}
	return r
}

func (s *Service) redirectionRoutes() {
	sv := s.srv
	sv.Handle("POST /onboarding", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Devices map[string]string `json:"devices"`
		}
		if err := decodeBody(r, &p); err != nil {
			reply(w, nil, err)
			return
		}
		if err := s.setBoolSetting("ONBOARDING_FINISHED", true); err != nil {
			reply(w, nil, err)
			return
		}
		for role, dev := range p.Devices {
			name, ok := enumName(role, roleNames)
			if id := roleRedirections[name]; ok && id != "" && dev != "" {
				s.setRedirectionDevice(id, dev)
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	sv.Handle("PUT /onboarding/configure", func(w http.ResponseWriter, r *http.Request) {
		if !s.boolSetting("ONBOARDING_FINISHED") {
			httpx.JSONStatus(w, 500, map[string]string{"error": "Cannot configure redirection while onboarding is not finished"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	sv.Handle("GET /streamRedirections", func(w http.ResponseWriter, r *http.Request) {
		out := []streamRedirection{}
		for _, id := range streamRedirectionIDs {
			out = append(out, s.streamRedirection(id))
		}
		httpx.JSON(w, out)
	})
	setStreamDevice := func(w http.ResponseWriter, r *http.Request, deviceID string) {
		id, ok := enumName(r.PathValue("id"), streamRedirectionNames)
		if !ok {
			reply(w, nil, argErrorf("The value '%s' is not valid.", r.PathValue("id")))
			return
		}
		if err := s.setSetting(upperSnake(id)+"_STREAM_MODE_DEVICE_ID", deviceID); err != nil {
			reply(w, nil, err)
			return
		}
		httpx.JSON(w, s.streamRedirection(id))
	}
	sv.Handle("PUT /streamRedirections/{id}/deviceId/{deviceId}", func(w http.ResponseWriter, r *http.Request) {
		setStreamDevice(w, r, r.PathValue("deviceId"))
	})
	sv.Handle("DELETE /streamRedirections/{id}/deviceId", func(w http.ResponseWriter, r *http.Request) { setStreamDevice(w, r, "") })
	sv.Handle("PUT /streamRedirections/{id}/redirections/{role}/isEnabled/{isEnabled}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := enumName(r.PathValue("id"), streamRedirectionNames)
		role, roleOK := enumName(r.PathValue("role"), roleNames)
		on, err := parseBool(r.PathValue("isEnabled"))
		if !ok || !roleOK || err != nil {
			reply(w, nil, argErrorf("Invalid stream redirection, role or value"))
			return
		}
		if err := s.setBoolSetting(upperSnake(id)+"_STREAM_MODE_"+upperSnake(role)+"_IS_ENABLED", on); err != nil {
			reply(w, nil, err)
			return
		}
		httpx.JSON(w, s.streamRedirection(id))
	})

	// The prompt to use a newly connected SteelSeries headset with Sonar.
	sv.Handle("GET /snoozableAlert/targetDeviceAlert", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"shouldShow": false, "device": nil})
	})
	sv.Handle("PUT /snoozableAlert/snoozeTargetDeviceAlert", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	sv.Handle("GET /classicRedirections", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, s.classicRedirections(classicRedirectionIDs))
	})
	setDevice := func(w http.ResponseWriter, r *http.Request, deviceID string) {
		id, ok := enumName(r.PathValue("id"), redirectionNames)
		if !ok {
			reply(w, nil, argErrorf("The value '%s' is not valid.", r.PathValue("id")))
			return
		}
		if err := s.setRedirectionDevice(id, deviceID); err != nil {
			reply(w, nil, err)
			return
		}
		httpx.JSON(w, s.classicRedirection(id))
	}
	sv.Handle("PUT /classicRedirections/{id}/deviceId/{deviceId}", func(w http.ResponseWriter, r *http.Request) {
		setDevice(w, r, r.PathValue("deviceId"))
	})
	sv.Handle("DELETE /classicRedirections/{id}/deviceId", func(w http.ResponseWriter, r *http.Request) { setDevice(w, r, "") })
	sv.Handle("PUT /classicRedirections/render/deviceId/{deviceId}", func(w http.ResponseWriter, r *http.Request) {
		for _, id := range renderRedirectionIDs {
			if err := s.setRedirectionDevice(id, r.PathValue("deviceId")); err != nil {
				reply(w, nil, err)
				return
			}
		}
		httpx.JSON(w, s.classicRedirections(renderRedirectionIDs))
	})
}
