package inputrec

// hidKeyboard is the kernel's HID keyboard usage → Linux key code table
// (drivers/hid/hid-input.c, hid_keyboard); 0 has no key.
var hidKeyboard = [256]uint16{
	0, 0, 0, 0, 30, 48, 46, 32, 18, 33, 34, 35, 23, 36, 37, 38,
	50, 49, 24, 25, 16, 19, 31, 20, 22, 47, 17, 45, 21, 44, 2, 3,
	4, 5, 6, 7, 8, 9, 10, 11, 28, 1, 14, 15, 57, 12, 13, 26,
	27, 43, 43, 39, 40, 41, 51, 52, 53, 58, 59, 60, 61, 62, 63, 64,
	65, 66, 67, 68, 87, 88, 99, 70, 119, 110, 102, 104, 111, 107, 109, 106,
	105, 108, 103, 69, 98, 55, 74, 78, 96, 79, 80, 81, 75, 76, 77, 71,
	72, 73, 82, 83, 86, 127, 116, 117, 183, 184, 185, 186, 187, 188, 189, 190,
	191, 192, 193, 194, 134, 138, 130, 132, 128, 129, 131, 137, 133, 135, 136, 113,
	115, 114, 0, 0, 0, 121, 0, 89, 93, 124, 92, 94, 95, 0, 0, 0,
	122, 123, 90, 91, 85, 0, 0, 0, 0, 0, 0, 0, 111, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 179, 180, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 111, 0, 0, 0, 0, 0, 0, 0,
	29, 42, 56, 125, 97, 54, 100, 126,
}

// consumerKeys are the media keys GG records on the consumer page (the
// volume keys are on the keyboard page, where GG's editor names them).
var consumerKeys = map[uint16]uint16{
	164: 0xcd, // play/pause
	163: 0xb5, // next
	165: 0xb6, // previous
	166: 0xb7, // stop
}

// keyUsage is the HID keyboard usage of each Linux key code (the first
// usage the kernel gives it).
var keyUsage = func() map[uint16]uint8 {
	m := map[uint16]uint8{}
	for usage, key := range hidKeyboard {
		if _, ok := m[key]; key != 0 && !ok {
			m[key] = uint8(usage)
		}
	}
	return m
}()

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
