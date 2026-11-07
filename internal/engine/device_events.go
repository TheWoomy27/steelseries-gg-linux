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
	if ev.kind&evGenericStatus != 0 {
		l.mu.Lock()
		if l.status == nil {
			l.status = map[string]any{}
		}
		for k, v := range ev.data {
			l.status[k] = v
		}
		l.mu.Unlock()
	}
	if w, ok := ev.data["wirelessDevConnStatusEvent"].(map[string]any); ok {
		e.emit("EVENT_WIRELESS_DEVICE_CONNECTION_STATUS", map[string]any{"deviceId": l.dbID, "isConnected": w["isConnected"], "physicalId": w["physicalId"]})
	}
	if ev.kind&(evDispatchToUI|evGenericStatus) != 0 {
		e.emit("device_event", map[string]any{"id": l.dbID, "product_id": l.cat.ProductID, "data": ev.data})
		e.emit("EVENT_DEVICE_STATUS_CHANGED", map[string]any{"deviceId": l.dbID, "status": ev.data})
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
		out["batteryLevels"] = map[string]any{"type": "single_battery", "statusKnown": true,
			"percent": l.battery.percent, "charging": l.battery.charging}
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
