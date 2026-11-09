package engine

import "github.com/steelseries/golisp"

// Sonar uses what Engine knows about SteelSeries headsets. A headset's spec
// registers its USB audio device with (register-mm-device-info pid audio
// features), where the features say whether ChatMix is set on the headset,
// and reports its ChatMix dial with chatMixEvent device events. One product
// can have several audio devices (the Arctis Nova 7 Gen 2 registers both
// its own and the upgraded dongle's).

const (
	mmFeatureChatMixFinite   = 1
	mmFeatureChatMixInfinite = 2
)

type mmDevice struct {
	product  uint32 // the device spec's product id
	features int64
}

// Headset is what Engine knows about a headset's audio device.
type Headset struct {
	Name        string // the Engine device's name (arctis_nova_7_gen2_tx)
	ChatMixDial bool   // ChatMix is set on the headset
	Wireless    bool   // Engine follows the headset's wireless link
	Connected   bool   // the link is up
}

func (e *Engine) registerMMDevice(pid uint32, data map[string]any) {
	audio, _ := number(data["audioDevice"])
	features, _ := number(data["features"])
	e.mu.Lock()
	if e.mmDevices == nil {
		e.mmDevices = map[uint32]mmDevice{}
	}
	e.mmDevices[uint32(audio)] = mmDevice{pid, int64(features)}
	e.mu.Unlock()
}

// OnChatMix registers fn for ChatMix dial moves, with game and chat levels
// from 0 to 100.
func (e *Engine) OnChatMix(fn func(audio uint32, game, chat int)) {
	e.mu.Lock()
	e.onChatMix = fn
	e.mu.Unlock()
}

// OnBatteryLow registers fn for a device whose battery runs low, with its
// name, battery level and whether Engine's low battery sound is on.
func (e *Engine) OnBatteryLow(fn func(device string, percent int, sound bool)) {
	e.mu.Lock()
	e.onBatteryLow = fn
	e.mu.Unlock()
}

// OnHeadsetLink registers fn for changes of a headset's wireless link.
func (e *Engine) OnHeadsetLink(fn func()) {
	e.mu.Lock()
	e.onHeadsetLink = fn
	e.mu.Unlock()
}

func (e *Engine) mmDevice(audio uint32) (mmDevice, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	mm, ok := e.mmDevices[audio]
	return mm, ok
}

// Headset reports what Engine knows about the headset with the given USB
// audio device.
func (e *Engine) Headset(audio uint32) (Headset, bool) {
	mm, ok := e.mmDevice(audio)
	if !ok {
		return Headset{}, false
	}
	h := Headset{ChatMixDial: mm.features&(mmFeatureChatMixFinite|mmFeatureChatMixInfinite) != 0}
	if l := e.liveByProduct(mm.product); l != nil {
		l.mu.Lock()
		h.Wireless, h.Connected = l.linkKnown, l.linkUp
		l.mu.Unlock()
		h.Name = l.cat.Name
	}
	return h, true
}

// ChatMixLevels reads the headset's ChatMix dial (get-chatmix-values).
func (e *Engine) ChatMixLevels(audio uint32) (game, chat int, ok bool) {
	mm, ok := e.mmDevice(audio)
	if !ok {
		return 0, 0, false
	}
	l := e.liveByProduct(mm.product)
	if l == nil {
		return 0, 0, false
	}
	for _, d := range l.decls() {
		if !d.HasFunction("get-chatmix-values") {
			continue
		}
		v, err := d.Call("get-chatmix-values")
		if err != nil {
			return 0, 0, false
		}
		if levels := golisp.ToArray(v); len(levels) == 2 && golisp.IntegerP(levels[0]) && golisp.IntegerP(levels[1]) {
			return int(golisp.IntegerValue(levels[0])), int(golisp.IntegerValue(levels[1])), true
		}
	}
	return 0, 0, false
}

// headsetEvent forwards a ChatMix dial event to Sonar, for each audio
// device of the product.
func (e *Engine) headsetEvent(pid uint32, data map[string]any) {
	c, ok := data["chatMixEvent"].(map[string]any)
	if !ok {
		return
	}
	game, ok1 := number(c["gameAttenuation"])
	chat, ok2 := number(c["chatAttenuation"])
	if !ok1 || !ok2 {
		return
	}
	e.mu.Lock()
	var audios []uint32
	for audio, mm := range e.mmDevices {
		if mm.product == pid {
			audios = append(audios, audio)
		}
	}
	fn := e.onChatMix
	e.mu.Unlock()
	for _, audio := range audios {
		if fn != nil {
			fn(audio, game, chat)
		}
	}
}

func number(v any) (int, bool) {
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
