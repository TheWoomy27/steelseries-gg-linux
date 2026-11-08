package sonar

import (
	"reflect"
	"testing"
)

func TestPairPorts(t *testing.T) {
	ports := func(base int, chs ...string) []pwPort {
		var out []pwPort
		for i, ch := range chs {
			out = append(out, pwPort{id: base + i, channel: ch})
		}
		return out
	}
	cases := []struct {
		name     string
		from, to []pwPort
		want     [][2]int
	}{
		{"stereo to stereo", ports(1, "FL", "FR"), ports(10, "FL", "FR"), [][2]int{{1, 10}, {2, 11}}},
		{"stereo to surround", ports(1, "FL", "FR"), ports(10, "FL", "FR", "FC", "LFE", "RL", "RR"), [][2]int{{1, 10}, {2, 11}}},
		{"stereo to mono", ports(1, "FL", "FR"), ports(10, "MONO"), [][2]int{{1, 10}, {2, 10}}},
		{"mono mic", ports(1, "MONO"), ports(10, "MONO"), [][2]int{{1, 10}}},
		{"stereo mic to the mono channel", ports(1, "FL", "FR"), ports(10, "MONO"), [][2]int{{1, 10}, {2, 10}}},
		{"no device ports", ports(1, "FL", "FR"), nil, nil},
	}
	for _, c := range cases {
		if got := pairPorts(c.from, c.to); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
