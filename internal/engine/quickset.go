package engine

// What GG's QuickSet profiles need from the Engine: the configuration each
// device runs now, the devices a profile can hold, and the configurations
// the user picks outside QuickSet, which the active profile takes on.

// ActiveConfigID is the configuration a device runs, "" if none.
func (e *Engine) ActiveConfigID(deviceID int) string {
	c, err := e.activeConfig(deviceID)
	if err != nil {
		return ""
	}
	return c.ID
}

// EligibleDevices are the connected devices a QuickSet profile can hold a
// configuration for.
func (e *Engine) EligibleDevices() []map[string]any {
	out := []map[string]any{}
	for _, d := range e.loadoutDevicesJSON(true) {
		out = append(out, map[string]any{
			"id": d["id"], "productId": d["productId"], "name": d["name"], "fullName": d["fullName"],
			"displayName": d["displayName"], "type": d["type"], "deviceTypeName": d["deviceTypeName"],
			"hideDeviceCard": d["hideDeviceCard"], "childDeviceName": d["childDeviceName"],
			"firstConnectionTime": d["firstConnectionTime"],
		})
	}
	return out
}

// OnConfigSelected registers fn for configurations the user deploys
// (switching or creating one), not those a profile deploys.
func (e *Engine) OnConfigSelected(fn func(deviceID int, configID string)) {
	e.mu.Lock()
	e.onConfigSelected = fn
	e.mu.Unlock()
}

func (e *Engine) configSelected(c *config) {
	e.mu.Lock()
	fn := e.onConfigSelected
	e.mu.Unlock()
	if fn != nil {
		fn(c.DeviceID, c.ID)
	}
}
