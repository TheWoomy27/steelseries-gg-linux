package sonar

import (
	"encoding/json"
	"fmt"
	"os/exec"
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
}

type pwObject struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Info struct {
		Props map[string]any `json:"props"`
	} `json:"info"`
}

func pwDump() ([]pwObject, error) {
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

// audioDevices lists the PipeWire sinks (Render) and sources (Capture).
func audioDevices() ([]audioDevice, error) {
	objs, err := pwDump()
	if err != nil {
		return nil, err
	}
	out := []audioDevice{}
	for _, o := range objs {
		if o.Type != "PipeWire:Interface:Node" {
			continue
		}
		p := o.Info.Props
		flow := ""
		switch p["media.class"] {
		case "Audio/Sink":
			flow = "Render"
		case "Audio/Source":
			flow = "Capture"
		default:
			continue
		}
		name, _ := p["node.name"].(string)
		desc, _ := p["node.description"].(string)
		if desc == "" {
			desc = name
		}
		channels, _ := p["audio.channels"].(float64)
		out = append(out, audioDevice{
			FriendlyName: desc, ID: name, DataFlow: flow, Role: "None", Channels: int(channels),
			DefaultRole: "Console", State: "Active",
		})
	}
	return out, nil
}
