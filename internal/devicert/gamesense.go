package devicert

import (
	"strings"

	"github.com/steelseries/golisp"
)

// GameSenseInfo is how GameSense sees a device, from its specification:
// its device types ("keyboard", "rgb-per-key-zones", "rgb-3-zone"...),
// the keys (HID codes) or zone numbers of each named zone, and the key
// codes it writes in place of others.
type GameSenseInfo struct {
	Types []string
	Zones map[string][]int
	Remap map[int]int
}

// GameSense evaluates the device's (device-type) and its interface's
// zones. Devices without GameSense lighting report no zones.
func (d *Decl) GameSense() (*GameSenseInfo, error) {
	info := &GameSenseInfo{Zones: map[string][]int{}, Remap: map[int]int{}}
	if base, err := d.Call("base-lighting-type"); err != nil || golisp.String(base) == "none" {
		return info, nil
	}
	types, err := d.Call("device-type")
	if err != nil {
		return nil, err
	}
	for _, t := range golisp.ToArray(types) {
		info.Types = append(info.Types, strings.TrimSuffix(golisp.String(t), ":"))
	}
	iface := golisp.Cons(golisp.Intern("interface"), nil)
	zones, err := d.rt.eval(golisp.Cons(golisp.Intern("zones:"), golisp.Cons(iface, nil)), d.Env)
	if err != nil || !golisp.FrameP(zones) {
		return info, nil
	}
	for k, v := range golisp.FrameValue(zones).Data {
		var codes []int
		for _, c := range golisp.ToArray(v) {
			if golisp.IntegerP(c) {
				codes = append(codes, int(golisp.IntegerValue(c)))
			}
		}
		info.Zones[strings.TrimSuffix(k, ":")] = codes
	}
	maps, err := d.rt.eval(golisp.Cons(golisp.Intern("hidcode-maps:"), golisp.Cons(iface, nil)), d.Env)
	if err == nil {
		for _, m := range golisp.ToArray(maps) {
			if p := golisp.ToArray(m); len(p) == 2 && golisp.IntegerP(p[0]) && golisp.IntegerP(p[1]) {
				info.Remap[int(golisp.IntegerValue(p[0]))] = int(golisp.IntegerValue(p[1]))
			}
		}
	}
	return info, nil
}
