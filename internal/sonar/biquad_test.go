package sonar

import (
	"math"
	"testing"
)

func TestBiquadResponses(t *testing.T) {
	near := func(got, want, tol float64) bool { return math.Abs(got-want) <= tol }
	cases := []struct {
		name       string
		f          biquad
		freq, want float64
	}{
		{"peak at center", newBiquad("peakingEQ", 1000, 12, 0.7071), 1000, 12},
		{"peak far away", newBiquad("peakingEQ", 1000, 12, 0.7071), 20, 0},
		{"cut at center", newBiquad("peakingEQ", 250, -6, 2), 250, -6},
		{"low shelf below", newBiquad("lowShelving", 100, 6, 0.7071), 20, 6},
		{"low shelf above", newBiquad("lowShelving", 100, 6, 0.7071), 5000, 0},
		{"high shelf above", newBiquad("highShelving", 8000, -9, 0.7071), 20000, -9},
		{"high shelf below", newBiquad("highShelving", 8000, -9, 0.7071), 100, 0},
		{"low pass at cutoff", newBiquad("lowPass", 1000, 0, 0.7071), 1000, -3.01},
		{"high pass passband", newBiquad("highPass", 100, 0, 0.7071), 5000, 0},
		{"identity", identity, 1234, 0},
		{"unknown type passes through", newBiquad("comb", 1000, 12, 1), 1000, 0},
	}
	for _, c := range cases {
		if got := c.f.response(c.freq); !near(got, c.want, 0.2) {
			t.Errorf("%s: %.2f dB at %g Hz, want %.2f", c.name, got, c.freq, c.want)
		}
	}
	if got := newBiquad("notchFilter", 1000, 0, 2).response(1000); got > -40 {
		t.Errorf("notch: %.1f dB at its center, want a deep cut", got)
	}
}
