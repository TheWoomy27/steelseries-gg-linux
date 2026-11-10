package inputrec

import "ggl/internal/hidkeys"

// consumerKeys are the media keys GG records on the consumer page (the
// volume keys are on the keyboard page, where GG's editor names them).
var consumerKeys = map[uint16]uint16{
	164: 0xcd, // play/pause
	163: 0xb5, // next
	165: 0xb6, // previous
	166: 0xb7, // stop
}

// keyUsage is the HID keyboard usage of a Linux key code.
func keyUsage(key uint16) (uint8, bool) { return hidkeys.Usage(key) }

// Linux mouse buttons and GG's numbers for them (1 left, 2 right, 3
// middle, 4 back, 5 forward).
var mouseButtons = map[uint16]uint16{
	0x110: 1, // BTN_LEFT
	0x111: 2, // BTN_RIGHT
	0x112: 3, // BTN_MIDDLE
	0x113: 4, // BTN_SIDE
	0x114: 5, // BTN_EXTRA
	0x116: 4, // BTN_BACK
	0x115: 5, // BTN_FORWARD
}
