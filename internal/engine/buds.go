package engine

import (
	"time"
)

// Earbuds (the Arctis GameBuds): the dongle reports each bud's battery and
// link, and the case, a device of its own while it is plugged in by USB,
// reports both buds' and its own battery and charging. GG shows them
// together on the dongle's device, as the Windows Engine's
// LeftRightCaseBatteryStatus and LeftRightCaseConnectionStatus.

type budsState struct {
	left, right, casePct    int // -1 unknown
	leftCase, rightCase     int // each bud's battery as the case reports it, -1 unknown
	leftChg, rightChg       bool
	caseChg                 bool
	leftUp, rightUp, linked bool // linked: the dongle reported the buds' links
}

// budsState is l's earbuds state, made on first use. Callers hold l.mu.
func (l *liveDevice) budsState() *budsState {
	if l.buds == nil {
		l.buds = &budsState{left: -1, right: -1, casePct: -1, leftCase: -1, rightCase: -1}
	}
	return l.buds
}

// percentOf reads a battery percentage: 1-100 is a level, anything else
// (0 for a bud the dongle can't reach) unknown.
func percentOf(v any) int {
	if n, ok := number(v); ok && n >= 1 && n <= 100 {
		return n
	}
	return -1
}

// battery takes the dongle's batteryEvent {leftBatteryPercent,
// rightBatteryPercent}.
func (b *budsState) battery(ev map[string]any) {
	b.left, b.right = percentOf(ev["leftBatteryPercent"]), percentOf(ev["rightBatteryPercent"])
}

// connection takes the dongle's connectionEvent {leftConnectionStatus,
// rightConnectionStatus}.
func (b *budsState) connection(ev map[string]any) {
	b.leftUp = ev["leftConnectionStatus"] == "PAIRED_CONNECTED"
	b.rightUp = ev["rightConnectionStatus"] == "PAIRED_CONNECTED"
	b.linked = true
}

// caseBattery takes the case's caseBatteryEvent.
func (b *budsState) caseBattery(ev map[string]any) {
	b.leftCase, b.rightCase = percentOf(ev["leftBatteryPercent"]), percentOf(ev["rightBatteryPercent"])
	b.casePct = percentOf(ev["caseBatteryPercent"])
	b.leftChg = ev["leftChargingStatus"] == "PLUGGED_IN_CHARGING"
	b.rightChg = ev["rightChargingStatus"] == "PLUGGED_IN_CHARGING"
	b.caseChg = ev["caseChargingStatus"] == "PLUGGED_IN_CHARGING"
}

// statuses renders the battery and connection statuses GG's device header
// shows. A bud's level is the dongle's, else the case's.
func (b *budsState) statuses(caseUp bool) (battery, link map[string]any) {
	level := func(dongle, inCase int) int {
		if dongle >= 0 {
			return dongle
		}
		if caseUp {
			return inCase
		}
		return -1
	}
	l, r, c := level(b.left, b.leftCase), level(b.right, b.rightCase), -1
	if caseUp {
		c = b.casePct
	}
	battery = map[string]any{"type": "left_right_case_battery",
		"leftStatusKnown": l >= 0, "leftPercent": max(l, 0), "leftCharging": caseUp && b.leftChg,
		"rightStatusKnown": r >= 0, "rightPercent": max(r, 0), "rightCharging": caseUp && b.rightChg,
		"caseStatusKnown": c >= 0, "casePercent": max(c, 0), "caseCharging": caseUp && b.caseChg}
	st := func(up bool) string {
		if up {
			return "connected"
		}
		return "not_connected"
	}
	link = map[string]any{"type": "left_right_case", "leftStatus": st(b.linked && b.leftUp),
		"rightStatus": st(b.linked && b.rightUp), "caseStatus": st(caseUp)}
	return battery, link
}

// budsDongle is the dongle device an earbuds case belongs to.
func (e *Engine) budsDongle(caseDev *liveDevice) *liveDevice {
	var dongle uint32
	if e.db.QueryRow(`SELECT transmitter_product_id FROM wireless_device_information WHERE receiver_id = ?`, caseDev.dbID).Scan(&dongle) != nil {
		return nil
	}
	return e.liveByProduct(dongle)
}

// budsCaseConnected reports whether the case of a dongle's earbuds is
// plugged in.
func (e *Engine) budsCaseConnected(dongle *liveDevice) bool {
	var caseID int
	if e.db == nil || e.db.QueryRow(`SELECT receiver_id FROM wireless_device_information WHERE transmitter_product_id = ?`, dongle.cat.ProductID).Scan(&caseID) != nil {
		return false
	}
	return e.liveByID(caseID) != nil
}

// emitStatus tells the client a device's statuses (battery, links) as GG's
// device header reads them.
func (e *Engine) emitStatus(l *liveDevice) {
	e.emit("EVENT_DEVICE_STATUS_CHANGED", map[string]any{"device": l.cat.Name, "newStatus": e.genericStatuses(l.cat.Name)})
}

// reread runs a device's connect steps again when its spec says it
// reconnected (reconnectDevice: earbuds out of their case), once for a
// burst of such events.
func (e *Engine) reread(l *liveDevice) {
	l.mu.Lock()
	if l.rereading || !l.ready {
		l.mu.Unlock()
		return
	}
	l.rereading = true
	l.mu.Unlock()
	go func() {
		time.Sleep(time.Second) // each bud reports itself
		if d := l.primary(); d != nil {
			e.deviceReady(l, d)
		}
		l.mu.Lock()
		l.rereading = false
		l.mu.Unlock()
	}()
}
