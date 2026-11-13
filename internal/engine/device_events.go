package engine

import (
	"context"
	"strconv"
)

// Device event kinds (InitDeviceEventTypeConstants).
const (
	evDispatchToUI       = 1
	evProcessInCore      = 2
	evProcessInCoreAsync = 4
	evSyncData           = 8
	evGenericStatus      = 16
)

type deviceEvent struct {
	productID uint32
	kind      int
	data      map[string]any
}

// DeviceEvent implements devicert.Hooks: device code reports status
// (battery, wireless link...) with (device-event PID KIND json).
func (e *Engine) DeviceEvent(pid uint32, kind string, data map[string]any) {
	if kind == "mmDeviceInfo" {
		e.registerMMDevice(pid, data)
		return
	}
	k, err := strconv.Atoi(kind)
	if err != nil {
		return
	}
	select {
	case e.deviceEvents <- deviceEvent{pid, k, data}:
	default:
	}
}

func (e *Engine) eventLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-e.deviceEvents:
			e.handleDeviceEvent(ev)
		}
	}
}

func (e *Engine) handleDeviceEvent(ev deviceEvent) {
	l := e.liveByProduct(ev.productID)
	if l == nil {
		return
	}
	l.mu.Lock()
	linkKnown, linkUp := l.linkKnown, l.linkUp
	if ev.kind&evGenericStatus != 0 {
		if l.status == nil {
			l.status = map[string]any{}
		}
		for k, v := range ev.data {
			l.status[k] = v
		}
	}
	l.trackStatus(ev.data)
	linkChanged := l.linkKnown != linkKnown || l.linkUp != linkUp
	becameLow, percent := l.batteryLow(ev.data)
	l.mu.Unlock()
	if becameLow {
		e.mu.Lock()
		fn := e.onBatteryLow
		e.mu.Unlock()
		app := e.appSettings()
		if off, _ := app["disableNotifications"].(bool); fn != nil && !off {
			name, _ := e.deviceJSON(l.cat, l)["display_name"].(string)
			sound, _ := app["playLowBatterySound"].(bool)
			fn(name, percent, sound)
		}
	}
	e.headsetEvent(ev.productID, ev.data)
	if linkChanged {
		e.mu.Lock()
		fn := e.onHeadsetLink
		e.mu.Unlock()
		if fn != nil {
			fn()
		}
	}
	if cs, ok := ev.data["colorSpinUpdate"].(map[string]any); ok {
		// The device says which colour or edition it is (Sensei and Rival
		// Pro, GameBuds): GG shows that variant's picture and name.
		name, _ := cs["colorSpin"].(string)
		force, _ := cs["forceUpdate"].(bool)
		if e.optimizeForChild(l, name, force) {
			l.mu.Lock()
			ready := l.ready
			l.mu.Unlock()
			if ready {
				e.emit("deviceConnected", e.deviceJSON(l.cat, l))
			}
		}
	}
	if w, ok := ev.data["wirelessDevConnStatusEvent"].(map[string]any); ok {
		e.emit("EVENT_WIRELESS_DEVICE_CONNECTION_STATUS", map[string]any{"deviceId": l.dbID, "isConnected": w["isConnected"], "physicalId": w["physicalId"]})
	}
	if ev.kind&(evDispatchToUI|evGenericStatus) != 0 {
		// The client matches device_event on a top-level id and reads the
		// event fields beside it.
		msg := map[string]any{"id": l.dbID}
		for k, v := range ev.data {
			msg[k] = v
		}
		e.emit("device_event", msg)
		e.emit("EVENT_DEVICE_STATUS_CHANGED", map[string]any{"deviceId": l.dbID, "device": l.cat.Name, "status": ev.data})
	}
}

// batteryLow follows the low battery flag specs pass to the core with the
// battery level (5% or less), saying whether the battery has just run low.
// Callers hold l.mu.
func (l *liveDevice) batteryLow(d map[string]any) (becameLow bool, percent int) {
	b, ok := d["coreBatteryUpdateEvent"].(map[string]any)
	if !ok {
		return false, 0
	}
	low, _ := b["isBatteryLow"].(bool)
	percent, _ = number(b["percentage"])
	becameLow, l.low = low && !l.low, low
	return becameLow, percent
}

// trackStatus records battery, charging and link state from the event
// shapes device specs emit. Callers hold l.mu.
func (l *liveDevice) trackStatus(d map[string]any) {
	num := func(v any) (int, bool) {
		switch x := v.(type) {
		case int64:
			return int(x), true
		case float64:
			return int(x), true
		case int:
			return x, true
		}
		return 0, false
	}
	battery := func() *batteryState {
		if l.battery == nil {
			l.battery = &batteryState{percent: -1}
		}
		return l.battery
	}
	if b, ok := d["battery_status"].(map[string]any); ok { // mice: {level, charging 0/1/2}
		if n, ok := num(b["level"]); ok {
			battery().percent = n
		}
		if n, ok := num(b["charging"]); ok {
			battery().charging = n == 1
		}
	}
	if b, ok := d["batteryEvent"].(map[string]any); ok { // headsets
		if n, ok := num(b["batteryPercent"]); ok {
			battery().percent = n
		}
	}
	if c, ok := d["chargingEvent"].(map[string]any); ok {
		battery().charging = c["chargingStatus"] == "PLUGGED_IN_CHARGING"
	}
	if c, ok := d["connectionEvent"].(map[string]any); ok {
		l.linkKnown, l.linkUp = true, c["connectionStatus"] == "PAIRED_CONNECTED"
	}
	if c, ok := d["connection_status"].(map[string]any); ok {
		if n, ok := num(c["status"]); ok {
			l.linkKnown, l.linkUp = true, n != 0
		}
	}
	if w, ok := d["wirelessDevConnStatusEvent"].(map[string]any); ok {
		if up, ok := w["isConnected"].(bool); ok {
			l.linkKnown, l.linkUp = true, up
		}
	}
}

// genericStatuses renders the battery / wireless link / firmware status
// cards the client shows for a device (generic_device_settings statuses).
func (e *Engine) genericStatuses(deviceName string) map[string]any {
	out := map[string]any{}
	var l *liveDevice
	e.mu.Lock()
	for _, x := range e.live {
		if x.cat.Name == deviceName {
			l = x
		}
	}
	e.mu.Unlock()
	if l == nil {
		return out
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.battery != nil {
		out["batteryLevels"] = map[string]any{"type": "single_battery", "statusKnown": l.battery.percent >= 0,
			"percent": max(l.battery.percent, 0), "charging": l.battery.charging}
	}
	if l.linkKnown {
		st := "not_connected"
		if l.linkUp {
			st = "connected"
		}
		out["wirelessConnection"] = map[string]any{"type": "radio_bt_wired", "twoFourGHZStatus": st,
			"btStatus": "not_connected", "wiredStatus": "not_connected", "uwbStatus": "not_connected"}
	}
	return out
}
