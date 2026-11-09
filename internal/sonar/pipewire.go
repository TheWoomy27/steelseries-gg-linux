package sonar

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// audioDevice is Sonar's AudioDevice: an output or input Sonar's channels
// can be routed to. Ids are PipeWire node names.
type audioDevice struct {
	FriendlyName     string `json:"friendlyName"`
	ID               string `json:"id"`
	DataFlow         string `json:"dataFlow"`
	Role             string `json:"role"`
	Channels         int    `json:"channels"`
	DefaultRole      string `json:"defaultRole"`
	FWUpdateRequired bool   `json:"fwUpdateRequired"`
	State            string `json:"state"`
	IsVad            bool   `json:"isVad"`

	serial string // object.serial, how WirePlumber's stream targets name nodes
}

type pwObject struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Info struct {
		Props      map[string]any `json:"props"`
		State      string         `json:"state"`          // nodes
		Direction  string         `json:"direction"`      // ports
		OutputPort int            `json:"output-port-id"` // links
		InputPort  int            `json:"input-port-id"`
		OutputNode int            `json:"output-node-id"`
		InputNode  int            `json:"input-node-id"`
		Params     struct {
			Props []struct {
				ChannelVolumes []float64 `json:"channelVolumes"`
				Mute           *bool     `json:"mute"`
			} `json:"Props"` // nodes
			Profile []struct {
				Name string `json:"name"`
			} `json:"Profile"` // devices
		} `json:"params"`
	} `json:"info"`
}

func pwDump() ([]pwObject, error) {
	if objs, ok := graph.snapshot(); ok {
		return objs, nil
	}
	out, err := exec.Command("pw-dump").Output()
	if err != nil {
		return nil, fmt.Errorf("pw-dump: %w", err)
	}
	var objs []pwObject
	if err := json.Unmarshal(out, &objs); err != nil {
		return nil, fmt.Errorf("pw-dump: %w", err)
	}
	return objs, nil
}

// audioDevices lists the PipeWire sinks (render) and sources (capture).
// Sonar's own devices carry their channel's role, and default devices
// their Windows default role (see defaultRole).
func audioDevices() ([]audioDevice, error) {
	objs, err := pwDump()
	if err != nil {
		return nil, err
	}
	sink, source, _ := defaultDevices()
	return activeProfileDevices(objs, devicesIn(objs, sink, source), sink, source), nil
}

// activeProfileDevices leaves out the nodes a sound card offers for its
// inactive profiles (see otherProfileNodes), but for the default devices.
func activeProfileDevices(objs []pwObject, devs []audioDevice, sink, source string) []audioDevice {
	hide := otherProfileNodes(objs)
	out := devs[:0:0]
	for _, d := range devs {
		if !hide[d.ID] || d.ID == sink || d.ID == source {
			out = append(out, d)
		}
	}
	return out
}

// otherProfileNodes are the idle nodes a sound card offers for profiles
// other than its active one. Some UCM cards (onboard USB audio, for one)
// expose every jack once per profile, HiFi, HiFi 5+1 and HiFi 7+1 alike,
// which Windows shows as a single endpoint each. A running node (a channel
// plays to it) is never hidden.
func otherProfileNodes(objs []pwObject) map[string]bool {
	active := map[string]string{} // device id -> active profile
	for _, o := range objs {
		if o.Type == "PipeWire:Interface:Device" && len(o.Info.Params.Profile) > 0 {
			active[strconv.Itoa(o.ID)] = o.Info.Params.Profile[0].Name
		}
	}
	type node struct {
		name, device, profile string
		running               bool
	}
	var nodes []node
	matching := map[string]bool{} // devices with nodes named after their active profile
	for _, o := range objs {
		if o.Type != "PipeWire:Interface:Node" {
			continue
		}
		name, _ := o.Info.Props["node.name"].(string)
		n := node{name, propString(o.Info.Props, "device.id"), propString(o.Info.Props, "device.profile.name"), o.Info.State == "running"}
		nodes = append(nodes, n)
		if a := active[n.device]; a != "" && strings.HasPrefix(n.profile, a+":") {
			matching[n.device] = true
		}
	}
	hide := map[string]bool{}
	for _, n := range nodes {
		if matching[n.device] && !strings.HasPrefix(n.profile, active[n.device]+":") && !n.running {
			hide[n.name] = true
		}
	}
	return hide
}

// devicesIn lists the sinks and sources in a pw-dump, given the default
// sink and source names.
func devicesIn(objs []pwObject, sink, source string) []audioDevice {
	out := []audioDevice{}
	for _, o := range objs {
		if o.Type != "PipeWire:Interface:Node" {
			continue
		}
		p := o.Info.Props
		flow := ""
		switch p["media.class"] {
		case "Audio/Sink":
			flow = "render"
		case "Audio/Source":
			flow = "capture"
		default:
			continue
		}
		name, _ := p["node.name"].(string)
		desc, _ := p["node.description"].(string)
		if desc == "" {
			desc = name
		}
		nch, _ := p["audio.channels"].(float64)
		d := audioDevice{FriendlyName: desc, ID: name, DataFlow: flow, Role: "none", Channels: int(nch),
			DefaultRole: "console", State: "active", serial: propString(p, "object.serial")}
		if name == sink || name == source {
			d.DefaultRole = "all"
		}
		if name == streamDevice {
			d.Role, d.IsVad = "stream", true
		}
		for _, c := range channels {
			if c.node != name {
				continue
			}
			d.Role, d.IsVad, d.DefaultRole = camel(c.role), true, "console"
			switch {
			case c.role == "Game" && sink == name, c.capture && source == name:
				d.DefaultRole = "multimedia"
			case c.role == "ChatRender" && sink == channels[0].node:
				d.DefaultRole = "communications"
			}
		}
		out = append(out, d)
	}
	return out
}

// steelSeriesDevice is Sonar's SteelSeriesAudioDevice: a SteelSeries audio
// device with its output and input, which onboarding preselects.
type steelSeriesDevice struct {
	Name              string  `json:"name"`
	ImageURL          string  `json:"imageUrl"`
	CaptureID         *string `json:"captureId"`
	RenderID          *string `json:"renderId"`
	HumanReadableName string  `json:"humanReadableName"`
}

// steelSeriesDevices lists the SteelSeries (USB vendor 1038) audio devices
// with their sink and source nodes.
func steelSeriesDevices() ([]steelSeriesDevice, error) {
	objs, err := pwDump()
	if err != nil {
		return nil, err
	}
	byDevice := map[float64]*steelSeriesDevice{}
	var order []float64
	for _, o := range objs {
		p := o.Info.Props
		if o.Type == "PipeWire:Interface:Device" && p["media.class"] == "Audio/Device" && p["device.vendor.id"] == "0x1038" {
			name, _ := p["device.product.name"].(string)
			if desc, _ := p["device.description"].(string); name == "" {
				name = desc
			}
			byDevice[float64(o.ID)] = &steelSeriesDevice{Name: name, HumanReadableName: name}
			order = append(order, float64(o.ID))
		}
	}
	for _, o := range objs {
		p := o.Info.Props
		dev, _ := p["device.id"].(float64)
		d := byDevice[dev]
		if o.Type != "PipeWire:Interface:Node" || d == nil {
			continue
		}
		name, _ := p["node.name"].(string)
		switch p["media.class"] {
		case "Audio/Sink":
			if d.RenderID == nil {
				d.RenderID = &name
			}
		case "Audio/Source":
			if d.CaptureID == nil {
				d.CaptureID = &name
			}
		}
	}
	out := []steelSeriesDevice{}
	for _, id := range order {
		if d := byDevice[id]; d.RenderID != nil || d.CaptureID != nil {
			out = append(out, *d)
		}
	}
	return out, nil
}

// firstSteelSeriesDevice picks the device onboarding preselects: one with
// both an output and an input, else an output, else an input.
func firstSteelSeriesDevice(devs []steelSeriesDevice) *steelSeriesDevice {
	for _, want := range []func(d steelSeriesDevice) bool{
		func(d steelSeriesDevice) bool { return d.RenderID != nil && d.CaptureID != nil },
		func(d steelSeriesDevice) bool { return d.RenderID != nil },
		func(d steelSeriesDevice) bool { return d.CaptureID != nil },
	} {
		for _, d := range devs {
			if want(d) {
				return &d
			}
		}
	}
	return nil
}
