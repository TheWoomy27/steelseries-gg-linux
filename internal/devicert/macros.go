package devicert

import (
	"encoding/binary"
	"fmt"

	"github.com/steelseries/golisp"
)

// Onboard macros: keyboards with macro memory (the Apex 7 and others
// built on base_apex_legacy) play macros themselves. When their key
// mappings are written, the spec calls convert-onboard-macro-bindings
// with the mappings and the trigger each one answers to. Every key bound
// to a macro in the configuration being written then gets the macro
// function (key codes pointing at the macro) and the macro goes into the
// macro area, as the Windows Engine's LibsteelConvertToOnboardMacros
// does:
//
//   - A macro is an 8-byte header (0x77, playback type, event count u16,
//     trigger direction, repeats, repeat delay u16) and 4 bytes per event.
//   - Events: mouse button {0, button mask, 0, down}; wheel {1, 0, 0,
//     ±1}; key {2, usage, page (1 keyboard, 12 consumer), down}; delay
//     {4, 0, ms u16} (longer delays are split); AC pan {12, 0, 0, ±1}.
//   - The key's mapping becomes {function, key codes [0 0 offset/4 u16]}.

// MacroEvent is a macro event as GG stores it (deviceinfo.DeviceEvent).
type MacroEvent struct {
	Type      uint8  `json:"type"`
	Page      uint8  `json:"page"`
	Code      uint16 `json:"code"`
	ExtraData int64  `json:"extraData"`
	Timestamp uint32 `json:"timestamp"`
}

// Macro event types.
const (
	EventMouseButton = 0
	EventMouseWheel  = 1
	EventKey         = 2
	EventDelay       = 4
	EventMouseHold   = 7 // a mouse button held while the trigger is
	EventKeyHold     = 8 // a key held while the trigger is
	EventACPan       = 12
)

// Trigger is the key or button a binding answers to (types.Trigger).
type Trigger struct {
	HIDCode   uint16 `json:"hidCode"`
	Page      uint8  `json:"page"`
	Type      uint8  `json:"type"`
	Direction int8   `json:"direction"`
}

// Playback is how a macro plays (domains.PlaybackOptions).
type Playback struct {
	Type        uint8  `json:"type"`
	NumRepeats  uint8  `json:"num_repeat"`
	RepeatDelay uint16 `json:"repeat_delay"`
}

// MacroBinding is a key of a configuration bound to a macro, with the
// macro's events.
type MacroBinding struct {
	Trigger  Trigger
	Playback Playback
	Events   []MacroEvent
}

// MacroSource gives the macro bindings of a configuration: configID, or
// the device's current configuration when it is "". Hooks that implement
// it get onboard macros written.
type MacroSource interface {
	MacroBindings(devID uint32, configID string) []MacroBinding
}

// KeyMapping is one entry of a keyboard's mappings (hid_code, function,
// key_codes).
type KeyMapping struct {
	HIDCode  uint16
	Function uint8
	KeyCodes [4]uint8
}

// encodeMacro turns a macro into its onboard header and events.
func encodeMacro(b *MacroBinding) (header [8]byte, items [][4]byte, err error) {
	if b.Playback.Type > 4 {
		return header, nil, fmt.Errorf("unknown playback type: %d", b.Playback.Type)
	}
	for _, ev := range b.Events {
		switch ev.Type {
		case EventMouseButton:
			if ev.ExtraData != 0 && ev.ExtraData != 1 || ev.Code == 0 || ev.Code > 8 {
				return header, nil, fmt.Errorf("unsupported mouse button press %d direction %d", ev.Code, ev.ExtraData)
			}
			items = append(items, [4]byte{0, 1 << (ev.Code - 1), 0, uint8(ev.ExtraData)})
		case EventMouseWheel, EventACPan:
			switch d := wheel(ev.ExtraData); d {
			case 0:
			case 1, -1:
				items = append(items, [4]byte{ev.Type, 0, 0, uint8(d)})
			default:
				return header, nil, fmt.Errorf("unsupported scroll %d", ev.ExtraData)
			}
		case EventKey:
			page := ev.Page
			if page == 7 {
				page = 1
			}
			if ev.Code == 0 || ev.ExtraData != 0 && ev.ExtraData != 1 || page != 1 && page != 12 {
				return header, nil, fmt.Errorf("unsupported keypress %d page %d direction %d", ev.Code, ev.Page, ev.ExtraData)
			}
			items = append(items, [4]byte{2, uint8(ev.Code), page, uint8(ev.ExtraData)})
		case EventDelay:
			ms := ev.ExtraData
			for ms > 0xffff {
				items = append(items, [4]byte{4, 0, 0xff, 0xff})
				ms -= 0xffff
			}
			items = append(items, [4]byte{4, 0, uint8(ms), uint8(ms >> 8)})
		case EventMouseHold:
			items = append(items, [4]byte{0, uint8(ev.Code), 0, 1})
		case EventKeyHold:
			items = append(items, [4]byte{2, uint8(ev.Code), ev.Page, 1})
		default:
			return header, nil, fmt.Errorf("onboard mapping does not support event type %d", ev.Type)
		}
	}
	header = [8]byte{0x77, b.Playback.Type + 1, 0, 0, uint8(b.Trigger.Direction), b.Playback.NumRepeats}
	binary.LittleEndian.PutUint16(header[2:], uint16(len(items)))
	binary.LittleEndian.PutUint16(header[6:], b.Playback.RepeatDelay)
	return header, items, nil
}

// wheel reads a wheel direction, which GG stores as 1 and -1 (or -1 as
// an unsigned byte or word).
func wheel(v int64) int {
	switch v {
	case 1:
		return 1
	case -1, 0xff, 0xffffffff:
		return -1
	case 0:
		return 0
	}
	return 2
}

// ConvertOnboardMacros points the mappings whose triggers have macro
// bindings at onboard macros and packs those macros into size bytes. A
// macro that can't go onboard (an event the keyboard can't play, or no
// room left) leaves its key's mapping alone; skipped lists why.
func ConvertOnboardMacros(mappings []KeyMapping, triggers []Trigger, bindings []MacroBinding, size int, fn uint8) (out []KeyMapping, data []byte, skipped []error) {
	data = make([]byte, size)
	out = make([]KeyMapping, len(mappings))
	offset := 0
	for i, m := range mappings {
		out[i] = m
		t := triggers[i]
		var b *MacroBinding
		for j := range bindings {
			bt := bindings[j].Trigger
			if bt.HIDCode == t.HIDCode && bt.Page == t.Page && bt.Type == t.Type {
				b = &bindings[j]
				break
			}
		}
		if b == nil {
			continue
		}
		header, items, err := encodeMacro(b)
		if err != nil {
			skipped = append(skipped, fmt.Errorf("key %d: %w", t.HIDCode, err))
			continue
		}
		if len(items) == 0 {
			// An empty macro leaves the key doing nothing.
			out[i] = KeyMapping{HIDCode: m.HIDCode}
			continue
		}
		if offset+8+4*len(items) >= size {
			skipped = append(skipped, fmt.Errorf("key %d: no room left for its macro (%d of %d bytes used)", t.HIDCode, offset, size))
			continue
		}
		start := offset
		copy(data[offset:], header[:])
		offset += 8
		for _, it := range items {
			copy(data[offset:], it[:])
			offset += 4
		}
		words := uint16(start / 4)
		out[i] = KeyMapping{HIDCode: m.HIDCode, Function: fn, KeyCodes: [4]uint8{0, 0, uint8(words), uint8(words >> 8)}}
	}
	return out, data, skipped
}

// keyMappings reads a spec's mapping frames ({hid_code: function:
// key_codes:}).
func keyMappings(l *golisp.Data) ([]KeyMapping, error) {
	var out []KeyMapping
	for c := l; golisp.NotNilP(c); c = golisp.Cdr(c) {
		m, ok := fromLisp(golisp.Car(c)).(map[string]any)
		if !ok {
			return nil, fmt.Errorf("a mapping is not a frame: %s", golisp.String(golisp.Car(c)))
		}
		k := KeyMapping{HIDCode: uint16(num(m["hid_code"])), Function: uint8(num(m["function"]))}
		codes, _ := m["key_codes"].([]any)
		for i := 0; i < len(codes) && i < 4; i++ {
			k.KeyCodes[i] = uint8(num(codes[i]))
		}
		out = append(out, k)
	}
	return out, nil
}

// bindingTriggers reads a spec's binding triggers ({hidCode: page:
// type:}).
func bindingTriggers(l *golisp.Data) ([]Trigger, error) {
	var out []Trigger
	for c := l; golisp.NotNilP(c); c = golisp.Cdr(c) {
		m, ok := fromLisp(golisp.Car(c)).(map[string]any)
		if !ok {
			return nil, fmt.Errorf("a binding trigger is not a frame: %s", golisp.String(golisp.Car(c)))
		}
		out = append(out, Trigger{HIDCode: uint16(num(m["hidCode"])), Page: uint8(num(m["page"])), Type: uint8(num(m["type"]))})
	}
	return out, nil
}

func num(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}
