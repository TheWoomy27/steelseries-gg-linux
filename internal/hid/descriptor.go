package hid

// Collection is a top-level application collection from a report
// descriptor. On Windows each one is its own HID device; on Linux a hidraw
// node covers a whole USB interface, so we keep the list per node.
type Collection struct {
	UsagePage uint16
	Usage     uint16
	ReportIDs []uint8
}

// Descriptor summarises a HID report descriptor.
type Descriptor struct {
	Collections []Collection
	Numbered    bool // any Report ID item present
	// Report lengths in bytes keyed by report ID, excluding the ID byte.
	Input   map[uint8]int
	Output  map[uint8]int
	Feature map[uint8]int
	// Keys are the input fields carrying keyboard keys (usage page 7).
	Keys []KeyField
	// Buttons are the input fields carrying buttons (usage page 9), one
	// bit each.
	Buttons []ButtonField
	// Extra are the vendor bytes SteelSeries mice add to their pointer
	// reports (usage page 0xffc1): the first repeats buttons 1-8, the
	// second carries the buttons past those, which GG calls extra
	// buttons, one bit each.
	Extra []ButtonField
}

// ButtonField is a run of button bits in an input report: button
// UsageMin is at BitOffset (after the report ID), the next one after it.
type ButtonField struct {
	ReportID  uint8
	BitOffset int
	Count     int
	UsageMin  uint16
}

// KeyField is an input field of keyboard usages: an array of key codes
// (boot keyboards) or a bitmap with one bit per key (NKRO).
type KeyField struct {
	ReportID           uint8
	BitOffset          int // after the report ID
	Size, Count        int
	UsageMin, UsageMax uint16
	Array              bool
}

// ParseDescriptor walks the short items of a report descriptor, tracking
// just enough global/local state to find top-level collections and report
// sizes.
func ParseDescriptor(b []byte) Descriptor {
	d := Descriptor{Input: map[uint8]int{}, Output: map[uint8]int{}, Feature: map[uint8]int{}}
	bits := map[string]map[uint8]int{"in": {}, "out": {}, "feat": {}}
	var (
		usageMin, usageMax uint16
		usagePage          uint16
		usage              uint16
		haveUsage          bool
		reportSize         int
		reportCount        int
		reportID           uint8
		depth              int
		stack              []struct{ page uint16 }
	)
	for i := 0; i < len(b); {
		prefix := b[i]
		if prefix == 0xfe { // long item
			if i+1 >= len(b) {
				break
			}
			i += 3 + int(b[i+1])
			continue
		}
		size := int(prefix & 3)
		if size == 3 {
			size = 4
		}
		if i+1+size > len(b) {
			break
		}
		var val uint32
		for j := 0; j < size; j++ {
			val |= uint32(b[i+1+j]) << (8 * j)
		}
		typ := (prefix >> 2) & 3
		tag := prefix >> 4
		i += 1 + size
		switch typ {
		case 0: // main
			switch tag {
			case 0x8, 0x9, 0xb: // input, output, feature
				key := map[uint8]string{0x8: "in", 0x9: "out", 0xb: "feat"}[tag]
				if tag == 0x8 && usagePage == 0x07 && val&1 == 0 && reportSize > 0 && reportCount > 0 {
					d.Keys = append(d.Keys, KeyField{ReportID: reportID, BitOffset: bits["in"][reportID], Size: reportSize, Count: reportCount,
						UsageMin: usageMin, UsageMax: usageMax, Array: val&2 == 0})
				}
				if tag == 0x8 && usagePage == 0xffc1 && val&1 == 0 && reportSize == 8 && reportCount > 0 && len(d.Collections) > 0 {
					if c := d.Collections[len(d.Collections)-1]; c.UsagePage == 1 && c.Usage == 2 {
						d.Extra = append(d.Extra, ButtonField{ReportID: reportID, BitOffset: bits["in"][reportID], Count: reportCount})
					}
				}
				if tag == 0x8 && usagePage == 0x09 && val&1 == 0 && val&2 != 0 && reportSize == 1 && reportCount > 0 {
					first := usageMin
					if first == 0 {
						first = 1
					}
					d.Buttons = append(d.Buttons, ButtonField{ReportID: reportID, BitOffset: bits["in"][reportID], Count: reportCount, UsageMin: first})
				}
				bits[key][reportID] += reportSize * reportCount
			case 0xa: // collection
				if depth == 0 && val == 1 {
					d.Collections = append(d.Collections, Collection{UsagePage: usagePage, Usage: usage})
				}
				stack = append(stack, struct{ page uint16 }{usagePage})
				depth++
			case 0xc: // end collection
				if depth > 0 {
					depth--
					stack = stack[:len(stack)-1]
				}
			}
			haveUsage = false
			usageMin, usageMax = 0, 0
		case 1: // global
			switch tag {
			case 0x0:
				usagePage = uint16(val)
			case 0x7:
				reportSize = int(val)
			case 0x8:
				reportID = uint8(val)
				d.Numbered = true
				if depth > 0 && len(d.Collections) > 0 {
					c := &d.Collections[len(d.Collections)-1]
					c.ReportIDs = append(c.ReportIDs, reportID)
				}
			case 0x9:
				reportCount = int(val)
			}
		case 2: // local
			switch tag {
			case 0x1:
				usageMin = uint16(val)
			case 0x2:
				usageMax = uint16(val)
			}
			if tag == 0x0 && !haveUsage {
				if size == 4 {
					usagePage = uint16(val >> 16)
				}
				usage = uint16(val)
				haveUsage = true
			}
		}
	}
	for id, n := range bits["in"] {
		d.Input[id] = (n + 7) / 8
	}
	for id, n := range bits["out"] {
		d.Output[id] = (n + 7) / 8
	}
	for id, n := range bits["feat"] {
		d.Feature[id] = (n + 7) / 8
	}
	return d
}

// Has reports whether the descriptor declares a top-level collection with
// the given usage page and usage.
func (d Descriptor) Has(page, usage uint16) bool {
	for _, c := range d.Collections {
		if c.UsagePage == page && c.Usage == usage {
			return true
		}
	}
	return false
}

// PressedKeys decodes the keys held down in an input report (report ID
// first, 0 for unnumbered reports).
func (d Descriptor) PressedKeys(r []byte) []uint8 {
	if len(r) == 0 {
		return nil
	}
	id, data := r[0], r[1:]
	bit := func(i int) bool { return i/8 < len(data) && data[i/8]&(1<<(i%8)) != 0 }
	var out []uint8
	for _, f := range d.Keys {
		if f.ReportID != id {
			continue
		}
		for i := 0; i < f.Count; i++ {
			off := f.BitOffset + i*f.Size
			if f.Array {
				var v uint32
				for b := 0; b < f.Size && b < 32; b++ {
					if bit(off + b) {
						v |= 1 << b
					}
				}
				u := uint32(f.UsageMin) + v
				if v != 0 && u <= 0xff && (f.UsageMax == 0 || u <= uint32(f.UsageMax)) && u > 3 {
					out = append(out, uint8(u))
				}
			} else if f.Size == 1 && bit(off) {
				if u := int(f.UsageMin) + i; u > 3 && u <= 0xff {
					out = append(out, uint8(u))
				}
			}
		}
	}
	return out
}

// PressedButtons decodes the buttons held down in an input report
// (report ID first, 0 for unnumbered reports), by number (1 is the left
// button).
func (d Descriptor) PressedButtons(r []byte) []uint8 {
	if len(r) == 0 {
		return nil
	}
	id, data := r[0], r[1:]
	var out []uint8
	for _, f := range d.Buttons {
		if f.ReportID != id {
			continue
		}
		for i := 0; i < f.Count; i++ {
			off := f.BitOffset + i
			if off/8 < len(data) && data[off/8]&(1<<(off%8)) != 0 {
				if n := int(f.UsageMin) + i; n <= 0xff {
					out = append(out, uint8(n))
				}
			}
		}
	}
	return out
}

// ExtraButtons decodes the extra buttons held down in a pointer report
// (report ID first), as their bits (1, 2, 4...).
func (d Descriptor) ExtraButtons(r []byte) []uint8 {
	if len(r) == 0 {
		return nil
	}
	id, data := r[0], r[1:]
	var out []uint8
	for _, f := range d.Extra {
		at := f.BitOffset/8 + 1
		if f.ReportID != id || f.BitOffset%8 != 0 || f.Count < 2 || at >= len(data) {
			continue
		}
		b := data[at]
		for bit := uint8(1); bit != 0; bit <<= 1 {
			if b&bit != 0 {
				out = append(out, bit)
			}
		}
	}
	return out
}
