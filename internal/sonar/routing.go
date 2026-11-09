package sonar

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Per-app routing. Windows Sonar lists the audio sessions on every device
// and moves an app by setting its per-process default device. On PipeWire
// an app's session is its stream node; moving it sets the stream's
// target.object metadata to the device's object.serial, which WirePlumber
// follows and remembers for the app.

type audioSession struct {
	ID                   string `json:"id"`
	ProcessName          string `json:"processName"`
	ProcessID            int    `json:"processId"`
	IsSystemSound        bool   `json:"isSystemSound"`
	State                string `json:"state"`
	DisplayName          string `json:"displayName"`
	IsRoutingErrorProne  bool   `json:"isRoutingErrorProne"`
	RoutingErrorDetected bool   `json:"routingErrorDetected"`
}

type deviceAudioSessions struct {
	DeviceID      string         `json:"deviceId"`
	Role          string         `json:"role"`
	DataFlow      string         `json:"dataFlow"`
	AudioSessions []audioSession `json:"audioSessions"`
}

// appStream is an application's playback or recording stream.
type appStream struct {
	node    int
	flow    string // render, capture
	device  string // the sink or source it is linked to, if any
	session audioSession
}

// syntheticPID stands in for the process id of streams that don't name
// their process (Sonar addresses apps by process id).
const syntheticPID = 1 << 30

// appStreams lists the applications' streams in a pw-dump with the device
// each plays to or records from. Sonar's own streams and recordings of an
// output (visualizers, screen recorders) are left out.
func appStreams(objs []pwObject) []appStream {
	class := map[int]string{}
	name := map[int]string{}
	clients := map[int]map[string]any{}
	for _, o := range objs {
		switch o.Type {
		case "PipeWire:Interface:Node":
			class[o.ID], _ = o.Info.Props["media.class"].(string)
			name[o.ID], _ = o.Info.Props["node.name"].(string)
		case "PipeWire:Interface:Client":
			clients[o.ID] = o.Info.Props
		}
	}
	sinks, sources := map[int][]int{}, map[int][]int{}
	for _, o := range objs {
		if o.Type == "PipeWire:Interface:Link" {
			sinks[o.Info.OutputNode] = append(sinks[o.Info.OutputNode], o.Info.InputNode)
			sources[o.Info.InputNode] = append(sources[o.Info.InputNode], o.Info.OutputNode)
		}
	}
	var out []appStream
	for _, o := range objs {
		if o.Type != "PipeWire:Interface:Node" || strings.HasPrefix(name[o.ID], "sonar_") {
			continue
		}
		var flow, devClass string
		var peers []int
		switch class[o.ID] {
		case "Stream/Output/Audio":
			flow, devClass, peers = "render", "Audio/Sink", sinks[o.ID]
		case "Stream/Input/Audio":
			flow, devClass, peers = "capture", "Audio/Source", sources[o.ID]
		default:
			continue
		}
		p := o.Info.Props
		// Recordings of a device's output and level meters (GG's own among
		// them) are not apps.
		if p["stream.capture.sink"] == true || p["stream.monitor"] == true {
			continue
		}
		// Streams from native PipeWire clients leave the process to their
		// client object.
		client, _ := p["client.id"].(float64)
		prop := func(key string) string {
			if v := propString(p, key); v != "" {
				return v
			}
			return propString(clients[int(client)], key)
		}
		device, monitor := "", false
		for _, n := range peers {
			switch class[n] {
			case devClass:
				device = name[n]
			case "Audio/Sink":
				monitor = true
			}
		}
		if monitor {
			continue
		}
		process := prop("application.process.binary")
		display := prop("application.name")
		if process == "" {
			process = display
		}
		if display == "" {
			display = process
		}
		if process == "" {
			continue
		}
		pid, _ := strconv.Atoi(prop("application.process.id"))
		if pid <= 0 {
			pid = syntheticPID + o.ID
		}
		state := "inactive"
		if o.Info.State == "running" {
			state = "active"
		}
		role := strings.ToLower(propString(p, "media.role"))
		out = append(out, appStream{node: o.ID, flow: flow, device: device, session: audioSession{
			ID: strconv.Itoa(o.ID), ProcessName: process, ProcessID: pid, State: state, DisplayName: display,
			IsSystemSound: role == "event" || role == "notification",
		}})
	}
	return out
}

func propString(p map[string]any, key string) string {
	switch v := p[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

// sessionsByDevice groups the linked streams by device, listing every
// device (Sonar's API does) with the sessions on it.
func sessionsByDevice(devs []audioDevice, streams []appStream) []deviceAudioSessions {
	out := make([]deviceAudioSessions, 0, len(devs))
	for _, d := range devs {
		ds := deviceAudioSessions{DeviceID: d.ID, Role: d.Role, DataFlow: d.DataFlow, AudioSessions: []audioSession{}}
		for _, s := range streams {
			if s.device == d.ID && s.flow == d.DataFlow {
				ds.AudioSessions = append(ds.AudioSessions, s.session)
			}
		}
		out = append(out, ds)
	}
	return out
}

func currentSessions() ([]audioDevice, []appStream, error) {
	objs, err := pwDump()
	if err != nil {
		return nil, nil, err
	}
	sink, source, _ := defaultDevices()
	return devicesIn(objs, sink, source), appStreams(objs), nil
}

func (s *Service) routingRoutes() {
	sv := s.srv
	sv.Handle("GET /AudioDeviceRouting", func(w http.ResponseWriter, r *http.Request) {
		devs, streams, err := currentSessions()
		reply(w, sessionsByDevice(devs, streams), err)
	})
	sv.Handle("GET /AudioDeviceRouting/{deviceDataFlow}/{processId}", func(w http.ResponseWriter, r *http.Request) {
		flow, pid, err := routingArgs(r)
		if err != nil {
			reply(w, nil, err)
			return
		}
		_, streams, err := currentSessions()
		device := ""
		for _, st := range streams {
			if st.flow == flow && st.session.ProcessID == pid && st.device != "" {
				device = st.device
				break
			}
		}
		reply(w, device, err)
	})
	sv.Handle("PUT /AudioDeviceRouting/{deviceDataFlow}/{deviceId}/{processId}", func(w http.ResponseWriter, r *http.Request) {
		flow, pid, err := routingArgs(r)
		if err != nil {
			reply(w, nil, err)
			return
		}
		if err := s.routeApp(pid, flow, r.PathValue("deviceId")); err != nil {
			reply(w, nil, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func routingArgs(r *http.Request) (flow string, pid int, err error) {
	flow, ok := enumName(r.PathValue("deviceDataFlow"), map[int]string{0: "render", 1: "capture"})
	if !ok {
		return "", 0, argErrorf("The value '%s' is not valid.", r.PathValue("deviceDataFlow"))
	}
	pid64, perr := strconv.ParseUint(r.PathValue("processId"), 10, 32)
	if perr != nil {
		return "", 0, argErrorf("The value '%s' is not valid.", r.PathValue("processId"))
	}
	return flow, int(pid64), nil
}

// routeApp moves the app's streams of the given direction to a device and,
// like Sonar, tells the client about the devices involved a second later.
func (s *Service) routeApp(pid int, flow, device string) error {
	devs, streams, err := currentSessions()
	if err != nil {
		return err
	}
	var target *audioDevice
	for i := range devs {
		if devs[i].ID == device {
			target = &devs[i]
		}
	}
	if target == nil {
		slog.Warn("sonar: app not routed, device not found", "device", device)
		return nil
	}
	affected := map[string]bool{device: true}
	for _, st := range streams {
		if st.session.ProcessID != pid || st.flow != flow {
			continue
		}
		if st.device != "" {
			affected[st.device] = true
		}
		if target.DataFlow != flow {
			continue
		}
		if out, err := pwCommand("pw-metadata", strconv.Itoa(st.node), "target.object", target.serial, "Spa:Id").CombinedOutput(); err != nil {
			return fmt.Errorf("pw-metadata: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	go func() {
		time.Sleep(time.Second)
		devs, streams, err := currentSessions()
		if err != nil {
			return
		}
		for _, ds := range sessionsByDevice(devs, streams) {
			if affected[ds.DeviceID] && ds.DataFlow == flow {
				s.ws.Broadcast("SONAR_EVENT_ROUTING_DATA", ds)
			}
		}
	}()
	return nil
}

type sessionKey struct{ device, id string }

// watch follows apps and devices while Sonar is on, until stop is closed:
// it tells the client when apps start and stop playing or recording, as
// Sonar's session notifications do, moves channels between fallback
// devices and sends the devices' volumes when they change.
func (s *Service) watch(stop chan struct{}) {
	var sessions map[sessionKey]deviceAudioSessions
	var devices map[string]presentDevice
	var volumes []deviceVolume
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		if objs, err := pwDump(); err == nil {
			sessions = s.sessionTick(objs, sessions)
			present := s.presentDevices(objs)
			s.fallbackMu.Lock()
			s.fallbackTick(present, devices)
			s.fallbackMu.Unlock()
			devices = present
			volumes = s.volumeTick(objs, volumes)
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// sessionTick sends the session opened and closed events since the last
// tick (nil on the first) and returns the active sessions.
func (s *Service) sessionTick(objs []pwObject, last map[sessionKey]deviceAudioSessions) map[sessionKey]deviceAudioSessions {
	devs := map[string]audioDevice{}
	for _, d := range devicesIn(objs, "", "") {
		devs[d.ID] = d
	}
	cur := map[sessionKey]deviceAudioSessions{}
	for _, st := range appStreams(objs) {
		d, ok := devs[st.device]
		if ok && st.session.State == "active" && !st.session.IsSystemSound {
			cur[sessionKey{st.device, st.session.ID}] = deviceAudioSessions{d.ID, d.Role, d.DataFlow, []audioSession{st.session}}
		}
	}
	if last != nil {
		for k, old := range last {
			if _, ok := cur[k]; !ok {
				old.AudioSessions[0].State = "expired"
				s.ws.Broadcast("SONAR_EVENT_AUDIO_SESSION_CLOSED_DATA", old)
			}
		}
		for k, now := range cur {
			if _, ok := last[k]; !ok {
				s.ws.Broadcast("SONAR_EVENT_AUDIO_SESSION_OPENED_DATA", now)
			}
		}
	}
	return cur
}
