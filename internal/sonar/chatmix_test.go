package sonar

import (
	"math"
	"testing"
)

func TestChatMixCurves(t *testing.T) {
	for _, c := range []struct{ balance, scaled float64 }{
		{0, 0}, {1, 1}, {-1, -1}, {0.5, 2.0 / 3}, {-0.5, -2.0 / 3}, {0.6, 0.75},
	} {
		if got := scaledBalance(c.balance); math.Abs(got-c.scaled) > 1e-9 {
			t.Errorf("scaledBalance(%g) = %g, want %g", c.balance, got, c.scaled)
		}
	}
	for _, c := range []struct {
		game, chat int
		balance    float64
		ok         bool
	}{
		{100, 100, 0, true}, {40, 100, 0.6, true}, {100, 30, -0.7, true}, {0, 100, 1, true},
		{50, 50, 0, false}, {101, 100, 0, false},
	} {
		b, ok := dialBalance(c.game, c.chat)
		if ok != c.ok || math.Abs(b-c.balance) > 1e-9 {
			t.Errorf("dialBalance(%d, %d) = %g, %v; want %g, %v", c.game, c.chat, b, ok, c.balance, c.ok)
		}
	}
}
