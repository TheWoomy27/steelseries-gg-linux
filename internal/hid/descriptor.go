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
}

// ParseDescriptor walks the short items of a report descriptor, tracking
// just enough global/local state to find top-level collections and report
// sizes.
func ParseDescriptor(b []byte) Descriptor {
	d := Descriptor{Input: map[uint8]int{}, Output: map[uint8]int{}, Feature: map[uint8]int{}}
	bits := map[string]map[uint8]int{"in": {}, "out": {}, "feat": {}}
	var (
		usagePage   uint16
		usage       uint16
		haveUsage   bool
		reportSize  int
		reportCount int
		reportID    uint8
		depth       int
		stack       []struct{ page uint16 }
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
