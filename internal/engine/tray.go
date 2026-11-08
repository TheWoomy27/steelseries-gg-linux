package engine

import "sort"

// The Engine's tray submenu ("Engine configs", taskbar_config_menu on
// Windows): each connected device the user has not hidden, with its
// configurations. Picking one deploys it.

type TrayDevice struct {
	ID       int
	Name     string
	Battery  int // percent, -1 when unknown
	Charging bool
	Configs  []TrayConfig
}

type TrayConfig struct {
	ID      string
	Name    string
	Current bool
}

func (e *Engine) TrayDevices() []TrayDevice {
	e.mu.Lock()
	live := make([]*liveDevice, 0, len(e.live))
	for _, l := range e.live {
		live = append(live, l)
	}
	e.mu.Unlock()
	sort.Slice(live, func(i, j int) bool { return live[i].dbID < live[j].dbID })

	var out []TrayDevice
	for _, l := range live {
		if l.cat.HideDeviceCard || l.cat.DeploymentOff || e.userDevice(l.dbID, false).Hidden {
			continue
		}
		cfgs, err := e.configsForDevice(l.dbID)
		if err != nil || len(cfgs) == 0 {
			continue
		}
		d := TrayDevice{ID: l.dbID, Name: l.cat.FullName, Battery: -1}
		if d.Name == "" {
			d.Name = l.cat.Name
		}
		l.mu.Lock()
		// A level reported before the wireless link dropped is stale.
		if l.battery != nil && (l.linkUp || !l.linkKnown) {
			d.Battery, d.Charging = l.battery.percent, l.battery.charging
		}
		l.mu.Unlock()
		sort.SliceStable(cfgs, func(i, j int) bool { return cfgs[i].Name < cfgs[j].Name })
		for _, c := range cfgs {
			d.Configs = append(d.Configs, TrayConfig{ID: c.ID, Name: c.Name, Current: c.LastDeployed == 1})
		}
		out = append(out, d)
	}
	return out
}

// trayEvents are the engine events after which the tray menu may be out of
// date.
var trayEvents = map[string]bool{
	"deviceConnected":             true,
	"deviceDisconnected":          true,
	"switchConfig":                true,
	"reloadConfigs":               true,
	"EVENT_DEVICE_STATUS_CHANGED": true,
}

// OnTrayChange registers a function to call when TrayDevices may have
// changed.
func (e *Engine) OnTrayChange(fn func()) {
	e.mu.Lock()
	e.trayChanged = fn
	e.mu.Unlock()
}
