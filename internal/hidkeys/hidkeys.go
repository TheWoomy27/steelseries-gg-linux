// Package hidkeys maps HID keyboard and consumer usages to Linux key
// codes and back, as the kernel's HID driver does.
package hidkeys

// Keyboard is the kernel's HID keyboard usage → Linux key code table
// (drivers/hid/hid-input.c, hid_keyboard); the kernel reports the usages
// at 0 as KEY_UNKNOWN.
var Keyboard = [256]uint16{
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
	29, 42, 56, 125, 97, 54, 100, 126, 164, 166, 165, 163, 161, 115, 114, 113,
	150, 158, 159, 128, 136, 177, 178, 176, 142, 152, 173, 140,
}

// Consumer maps the media keys GG uses on the consumer page to Linux key
// codes.
var Consumer = map[uint16]uint16{
	0xcd: 164, // play/pause
	0xb5: 163, // next
	0xb6: 165, // previous
	0xb7: 166, // stop
	0xe2: 113, // mute
	0xe9: 115, // volume up
	0xea: 114, // volume down
}

// Usage is the HID keyboard usage of a Linux key code (the first usage
// the kernel gives it).
func Usage(key uint16) (uint8, bool) {
	u, ok := usages[key]
	return u, ok
}

var usages = func() map[uint16]uint8 {
	m := map[uint16]uint8{}
	for usage, key := range Keyboard {
		if _, ok := m[key]; key != 0 && !ok {
			m[key] = uint8(usage)
		}
	}
	return m
}()

// Spare lists keyboard usages with no key of their own (reserved ones
// first): the kernel reports them as KEY_UNKNOWN, which has no key symbol
// and that nothing on the desktop acts on, so a device can send them for
// ggd to see.
var Spare = func() []uint8 {
	var out []uint8
	for _, r := range [][2]int{{0xa5, 0xdf}, {0x95, 0xa4}} {
		for u := r[0]; u <= r[1]; u++ {
			if Keyboard[u] == 0 {
				out = append(out, uint8(u))
			}
		}
	}
	return out
}()

// Button is the Linux code of GG's mouse button n (1 left, 2 right, 3
// middle, 4 back, 5 forward, 6-8 extra), 0 for none.
func Button(n uint16) uint16 {
	if n >= 1 && n <= 8 {
		return 0x10f + n
	}
	return 0
}
