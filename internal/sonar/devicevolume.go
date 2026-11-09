package sonar

import (
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"

	"ggl/internal/httpx"
)

// Device volumes are the volumes of the classic redirections' devices
// (Sonar's VolumeService). On Linux they are the nodes' own volumes on the
// cubic scale desktop volume controls show, as wpctl reads and sets them.

type deviceVolume struct {
	DeviceID string  `json:"deviceId"`
	Volume   float64 `json:"volume"`
}

// nodeVolumes reads the nodes' volumes by node name.
func nodeVolumes(objs []pwObject) map[string]float64 {
	out := map[string]float64{}
	for _, o := range objs {
		if o.Type != "PipeWire:Interface:Node" {
			continue
		}
		name, _ := o.Info.Props["node.name"].(string)
		for _, p := range o.Info.Params.Props {
			if len(p.ChannelVolumes) == 0 {
				continue
			}
			sum := 0.0
			for _, v := range p.ChannelVolumes {
				sum += v
			}
			out[name] = math.Cbrt(sum / float64(len(p.ChannelVolumes)))
			break
		}
	}
	return out
}

// deviceVolumes lists the classic redirections' devices, each once, with
// their volumes (0 for a missing device).
func (s *Service) deviceVolumes(objs []pwObject) []deviceVolume {
	vols := nodeVolumes(objs)
	devs := s.redirectionDevices()
	out := []deviceVolume{}
	var seen []string
	for _, id := range classicRedirectionIDs {
		d := devs[id]
		if d == "" || slices.Contains(seen, d) {
			continue
		}
		seen = append(seen, d)
		out = append(out, deviceVolume{d, vols[d]})
	}
	return out
}

// volumeTick sends the device volumes when they change.
func (s *Service) volumeTick(objs []pwObject, last []deviceVolume) []deviceVolume {
	cur := s.deviceVolumes(objs)
	if last != nil && !slices.Equal(cur, last) {
		s.ws.Broadcast("SONAR_EVENT_DEVICE_VOLUMES_UPDATE", cur)
	}
	return cur
}

func (s *Service) deviceVolumeRoutes() {
	sv := s.srv
	sv.Handle("GET /volumeSettings/devices/volumes", func(w http.ResponseWriter, r *http.Request) {
		objs, err := pwDump()
		if err != nil {
			reply(w, nil, err)
			return
		}
		httpx.JSON(w, s.deviceVolumes(objs))
	})
	sv.Handle("PUT /volumeSettings/devices/{deviceId}/volume/{volume}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("deviceId")
		vol, err := strconv.ParseFloat(r.PathValue("volume"), 64)
		if err != nil {
			reply(w, nil, argErrorf("The value '%s' is not valid.", r.PathValue("volume")))
			return
		}
		devs := s.redirectionDevices()
		if !slices.ContainsFunc(classicRedirectionIDs, func(r string) bool { return devs[r] == id }) {
			reply(w, nil, argErrorf("Unknown audio device '%s' (Parameter 'deviceId')", id))
			return
		}
		ids, err := nodeIDs()
		if err == nil && ids[id] == 0 {
			err = fmt.Errorf("no audio device %s", id)
		}
		if err != nil {
			reply(w, nil, err)
			return
		}
		vol = math.Max(0, math.Min(1, vol))
		if out, err := pwCommand("wpctl", "set-volume", strconv.Itoa(ids[id]), strconv.FormatFloat(vol, 'f', 4, 64)).CombinedOutput(); err != nil {
			reply(w, nil, fmt.Errorf("wpctl: %v: %s", err, out))
			return
		}
		httpx.JSON(w, vol)
	})
}
