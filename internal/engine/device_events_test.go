package engine

import "testing"

func TestBatteryLowOnce(t *testing.T) {
	l := &liveDevice{}
	ev := func(pct int64, low bool) map[string]any {
		return map[string]any{"coreBatteryUpdateEvent": map[string]any{"productId": int64(0x1852), "percentage": pct, "isBatteryLow": low}}
	}
	for i, step := range []struct {
		data map[string]any
		want bool
	}{
		{ev(20, false), false},
		{ev(5, true), true},
		{ev(5, true), false}, // already announced
		{map[string]any{"battery_status": map[string]any{"level": int64(5)}}, false},
		{ev(30, false), false}, // charged
		{ev(5, true), true},    // low again
	} {
		if got, pct := l.batteryLow(step.data); got != step.want {
			t.Errorf("step %d: became low %v (%d%%), want %v", i, got, pct, step.want)
		}
	}
}
