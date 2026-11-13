package engine

import "testing"

func TestBudsStatuses(t *testing.T) {
	l := &liveDevice{}
	b := l.budsState()
	// In the ears: the dongle knows both buds; the case isn't plugged in.
	b.connection(map[string]any{"leftConnectionStatus": "PAIRED_CONNECTED", "rightConnectionStatus": "PAIRED_CONNECTED"})
	b.battery(map[string]any{"leftBatteryPercent": int64(80), "rightBatteryPercent": int64(75)})
	bat, link := b.statuses(false)
	if bat["leftPercent"] != 80 || bat["rightPercent"] != 75 || bat["leftStatusKnown"] != true || bat["caseStatusKnown"] != false {
		t.Errorf("in ears: %v", bat)
	}
	if link["leftStatus"] != "connected" || link["rightStatus"] != "connected" || link["caseStatus"] != "not_connected" {
		t.Errorf("in ears: %v", link)
	}
	// Back in the plugged-in case: the dongle loses them, the case reports
	// them charging.
	b.connection(map[string]any{"leftConnectionStatus": "NOT_CONNECTED", "rightConnectionStatus": "NOT_CONNECTED"})
	b.battery(map[string]any{"leftBatteryPercent": int64(0), "rightBatteryPercent": int64(0)})
	b.caseBattery(map[string]any{"leftBatteryPercent": int64(81), "rightBatteryPercent": int64(76), "caseBatteryPercent": int64(60),
		"leftChargingStatus": "PLUGGED_IN_CHARGING", "rightChargingStatus": "PLUGGED_IN_CHARGING", "caseChargingStatus": "DISCHARGING"})
	bat, link = b.statuses(true)
	if bat["leftPercent"] != 81 || bat["leftCharging"] != true || bat["casePercent"] != 60 || bat["caseCharging"] != false {
		t.Errorf("in case: %v", bat)
	}
	if link["leftStatus"] != "not_connected" || link["caseStatus"] != "connected" {
		t.Errorf("in case: %v", link)
	}
}
