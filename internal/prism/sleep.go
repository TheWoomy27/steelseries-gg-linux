package prism

import (
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Prism dims the lighting (or turns it off) while the monitors sleep, as
// each configuration's dimFactorOnMonitorSleep says. Hyprland tells
// whether its monitors are powered (DPMS); elsewhere they count as awake.

var sleepState struct {
	sync.Mutex
	checked time.Time
	asleep  bool
}

// monitorsAsleep reports whether every monitor is off, checking at most
// every few seconds.
func monitorsAsleep() bool {
	st := &sleepState
	st.Lock()
	defer st.Unlock()
	if time.Since(st.checked) < 3*time.Second {
		return st.asleep
	}
	st.checked = time.Now()
	st.asleep = false
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") == "" {
		return false
	}
	out, err := exec.Command("hyprctl", "-j", "monitors").Output()
	if err != nil {
		return false
	}
	var ms []struct {
		DPMS bool `json:"dpmsStatus"`
	}
	if json.Unmarshal(out, &ms) != nil || len(ms) == 0 {
		return false
	}
	for _, m := range ms {
		if m.DPMS {
			return false
		}
	}
	st.asleep = true
	return true
}

// usesDimming reports whether a layer dims anything while the monitors
// sleep (so whether they need watching).
func usesDimming(g *Group) bool {
	if g.GlobalConfig.DimFactorOnMonitorSleep > 0 {
		return true
	}
	for i := range g.DeviceTypeConfigs {
		if g.DeviceTypeConfigs[i].Config.DimFactorOnMonitorSleep > 0 {
			return true
		}
	}
	for i := range g.PerKeyConfigs {
		if g.PerKeyConfigs[i].Config.DimFactorOnMonitorSleep > 0 {
			return true
		}
	}
	for i := range g.DeviceConfigs {
		if g.DeviceConfigs[i].Config.DimFactorOnMonitorSleep > 0 {
			return true
		}
	}
	return false
}
