package devicert

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/steelseries/golisp"
)

// PrismSync lighting: a device spec describes how to colour its lights
// directly, frame by frame. get-prism-sync-custom-zone-write builds the
// reports to send (with serialize-api-to-bytearray), and
// get-prism-sync-zone-data lists the lit zones: where each sits and at
// which byte offsets of those serialized reports its red, green and blue
// go.
//
// serialize-api-to-bytearray writes each report behind an 8-byte header,
// which the zone offsets count: the HID chunk type (uint16), a flags byte
// (bit 0: the chunk reads the device's reply), a reserved byte and the
// chunk size (uint32), all little endian.

const prismHeaderLen = 8

// PrismZone is one lit zone of a device.
type PrismZone struct {
	X, Y             uint16
	BitmapX, BitmapY uint8
	HidCode          uint8
	LinearIndex      uint8
	Write            int // which of the device's zone writes
	Red, Green, Blue int // byte offsets in that write's report
}

// PrismWrite is one report of a PrismSync frame.
type PrismWrite struct {
	Type, Size int
	Read       bool
	Data       []byte
}

// PrismData is how a device takes PrismSync frames.
type PrismData struct {
	Supported, Whitelisted bool
	StepMs, CalcOffsetMs   int
	CoexistsWithSequencer  bool
	HasBitmap, HasLinear   bool
	HasHidCodes            bool
	CoordinatePlane        int
	Region                 string
	Zones                  []PrismZone
	ZoneWrites, InitWrites []PrismWrite
}

// prismHeader builds a serialized report's header.
func prismHeader(chunkType, size int, read bool) []byte {
	h := make([]byte, prismHeaderLen)
	binary.LittleEndian.PutUint16(h[0:], uint16(chunkType))
	if read {
		h[2] = 1
	}
	binary.LittleEndian.PutUint32(h[4:], uint32(size))
	return h
}

// parsePrismWrites splits serialized reports and returns where each
// report's data starts in b.
func parsePrismWrites(b []byte) ([]PrismWrite, []int, error) {
	var out []PrismWrite
	var starts []int
	for off := 0; off < len(b); {
		if len(b)-off < prismHeaderLen {
			return nil, nil, fmt.Errorf("truncated prism write at %d", off)
		}
		typ := int(binary.LittleEndian.Uint16(b[off:]))
		read := b[off+2]&1 != 0
		size := int(binary.LittleEndian.Uint32(b[off+4:]))
		start := off + prismHeaderLen
		end := start + size
		if size <= 0 || end > len(b) {
			return nil, nil, fmt.Errorf("bad prism write size %d at %d", size, off)
		}
		out = append(out, PrismWrite{Type: typ, Size: size, Read: read, Data: append([]byte(nil), b[start:end]...)})
		starts = append(starts, start)
		off = end
	}
	return out, starts, nil
}

// serializeForPrism encodes a request through its api's struct and puts
// the api's first HID chunk in front, padded to the chunk size.
func (d *Decl) serializeForPrism(name string, values map[string]any) ([]byte, error) {
	st := d.Struct(name)
	if st == nil {
		return nil, fmt.Errorf("serialize: unknown struct %s", name)
	}
	vals := st.Defaults(instance)
	for k, x := range values {
		vals[k] = x
	}
	payload, err := st.Encode(instance, st.Outgoing, vals)
	if err != nil {
		return nil, err
	}
	typ, size, read := HIDIO, len(payload), false
	if api := d.Api(name); api != nil {
		for _, cmd := range api.Write {
			if cmd.Protocol != ProtoHID || len(cmd.Groups) == 0 || len(cmd.Groups[0].Chunks) == 0 {
				continue
			}
			c := cmd.Groups[0].Chunks[0]
			typ, size = int(c.Type), int(c.Size)
			read = c.SetReadBit && api.MixesReadsWrites
			break
		}
	}
	if size < len(payload) {
		size = len(payload)
	}
	out := prismHeader(typ, size, read)
	out = append(out, payload...)
	return append(out, make([]byte, size-len(payload))...), nil
}

// prismZoneForm parses a (prism-sync-zone ...) form, written by hand in a
// spec's quoted list or returned by the builtin of the same name.
func prismZoneForm(f *golisp.Data) (PrismZone, int, bool) {
	if !golisp.PairP(f) {
		return PrismZone{}, 0, false
	}
	kind := golisp.StringValue(golisp.Car(f))
	var n []int64
	for c := golisp.Cdr(f); golisp.NotNilP(c); c = golisp.Cdr(c) {
		n = append(n, golisp.IntegerValue(golisp.Car(c)))
	}
	at := func(i int) int64 {
		if i < len(n) {
			return n[i]
		}
		return 0
	}
	var z PrismZone
	var buf int64
	switch kind {
	case "prism-sync-zone": // x y red green blue buffer
		z = PrismZone{X: uint16(at(0)), Y: uint16(at(1)), Red: int(at(2)), Green: int(at(3)), Blue: int(at(4))}
		buf = at(5)
	case "prism-sync-zone-bitmap-hid-code": // x y bitmap-x bitmap-y hid red green blue buffer
		z = PrismZone{X: uint16(at(0)), Y: uint16(at(1)), BitmapX: uint8(at(2)), BitmapY: uint8(at(3)), HidCode: uint8(at(4)),
			Red: int(at(5)), Green: int(at(6)), Blue: int(at(7))}
		buf = at(8)
	case "prism-sync-zone-linear": // x y index red green blue buffer
		z = PrismZone{X: uint16(at(0)), Y: uint16(at(1)), LinearIndex: uint8(at(2)), Red: int(at(3)), Green: int(at(4)), Blue: int(at(5))}
		buf = at(6)
	default:
		return PrismZone{}, 0, false
	}
	return z, int(buf), true
}

func (d *Decl) prismBool(fn string) bool {
	v, err := d.Call(fn)
	return err == nil && golisp.BooleanValue(v)
}

func (d *Decl) prismInt(fn string, def int) int {
	if v, err := d.Call(fn); err == nil && golisp.IntegerP(v) {
		return int(golisp.IntegerValue(v))
	}
	return def
}

// PrismData reads how the device takes PrismSync frames. It runs the
// spec's prism-sync-pre-init first, which may read the device (an Apex
// reads its keyboard layout).
func (d *Decl) PrismData() (*PrismData, error) {
	p := &PrismData{Supported: d.SupportsPrismSync, Whitelisted: d.PrismSyncV2Whitelisted, StepMs: 33}
	if !p.Supported {
		return p, nil
	}
	if d.HasFunction("prism-sync-pre-init") {
		if _, err := d.Call("prism-sync-pre-init"); err != nil {
			return nil, fmt.Errorf("prism-sync-pre-init: %w", err)
		}
	}
	p.StepMs = d.prismInt("get-prism-sync-step-ms", 33)
	p.CalcOffsetMs = d.prismInt("get-prism-sync-calculation-offset-ms", 0)
	p.CoexistsWithSequencer = d.prismBool("get-prism-sync-coexists-with-sequencer")
	p.HasBitmap = d.prismBool("get-prism-sync-has-bitmap-coordinates")
	p.HasLinear = d.prismBool("get-prism-sync-is-linear-device")
	p.HasHidCodes = d.prismBool("get-prism-sync-has-hid-codes")
	p.CoordinatePlane = d.prismInt("get-prism-sync-coordinate-plane", 0)
	if v, err := d.Call("get-prism-sync-region"); err == nil && golisp.StringP(v) {
		p.Region = golisp.StringValue(v)
	}

	v, err := d.Call("get-prism-sync-custom-zone-write")
	if err != nil {
		return nil, fmt.Errorf("get-prism-sync-custom-zone-write: %w", err)
	}
	writes, starts, err := parsePrismWrites(bytesOf(v))
	if err != nil {
		return nil, err
	}
	p.ZoneWrites = writes
	if v, err := d.Call("get-prism-sync-custom-init-write"); err == nil {
		if iw, _, err := parsePrismWrites(bytesOf(v)); err == nil {
			p.InitWrites = iw
		}
	}

	zv, err := d.Call("get-prism-sync-zone-data")
	if err != nil {
		return nil, fmt.Errorf("get-prism-sync-zone-data: %w", err)
	}
	for c := zv; golisp.NotNilP(c); c = golisp.Cdr(c) {
		z, _, ok := prismZoneForm(golisp.Car(c))
		if !ok {
			continue
		}
		// The offsets count the serialized writes as a whole: find the
		// write holding them and make them relative to its report.
		z.Write = -1
		for i := len(starts) - 1; i >= 0; i-- {
			if z.Red >= starts[i] {
				z.Write = i
				z.Red, z.Green, z.Blue = z.Red-starts[i], z.Green-starts[i], z.Blue-starts[i]
				break
			}
		}
		if z.Write < 0 || z.Blue >= len(writes[z.Write].Data) || z.Red < 0 || z.Green < 0 {
			continue
		}
		p.Zones = append(p.Zones, z)
	}
	return p, nil
}

// WritePrism sends a PrismSync frame (or init writes) to the device.
func (d *Decl) WritePrism(writes []PrismWrite) error {
	d.prismIO.Do(func() { d.prismTiming = d.newIOState() })
	st := &ioState{timeBetween: d.prismTiming.timeBetween, timeout: d.prismTiming.timeout}
	for i, w := range writes {
		if i > 0 {
			if wait := st.timeBetween - time.Since(st.lastWrite); wait > 0 {
				time.Sleep(wait)
			}
		}
		_, err := d.rt.transport.Chunk(d.target(), w.Type, w.Size, w.Data, w.Read, st.timeBetween, st.timeout)
		st.lastWrite = time.Now()
		if err != nil && w.Type != HIDIONoFail {
			return err
		}
	}
	return nil
}
