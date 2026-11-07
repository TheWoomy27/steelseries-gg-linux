package devicert

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"

	"github.com/steelseries/golisp"
)

// Field mirrors support.DeviceField: one member of a (struct ...).
type Field struct {
	Name          string
	Type          string // uint8 uint16 uint24 uint32 int8 int16 int24 int32 float32 bytearray varstring, or a struct name
	Aligned       bool
	Size          int // bytes; for bytearray from (size N)
	Repeat        int // (repeat N), 0 when not repeated
	IsConstant    bool
	Constant      uint32
	Range         *[2]int64
	Excluding     *[2]int64
	Values        []int64
	Deferred      *golisp.Data
	ToJSON        *golisp.Data
	FromJSON      *golisp.Data
	nested        *Struct
	ConstReplaced bool
}

// Section is the ordered field list for one direction of a struct.
type Section struct {
	Fields []*Field
}

// Struct mirrors support.DeviceStructure.
type Struct struct {
	Name     string
	Aligned  bool
	Outgoing *Section
	Incoming *Section
}

func scalarSize(t string) int {
	switch t {
	case "uint8", "int8":
		return 1
	case "uint16", "int16":
		return 2
	case "uint24", "int24":
		return 3
	case "uint32", "int32", "float32":
		return 4
	}
	return 0
}

func (f *Field) elemSize() int {
	if n := scalarSize(f.Type); n > 0 {
		return n
	}
	switch f.Type {
	case "bytearray":
		return f.Size
	case "varstring":
		return 0
	}
	if f.nested != nil {
		return f.nested.Outgoing.size(f.nested.Aligned)
	}
	return 0
}

func (f *Field) count() int {
	if f.Repeat > 0 {
		return f.Repeat
	}
	return 1
}

func align(off, n int) int {
	if n <= 1 {
		return off
	}
	if n > 4 {
		n = 4
	}
	if r := off % n; r != 0 {
		return off + n - r
	}
	return off
}

func (s *Section) size(aligned bool) int {
	off := 0
	for _, f := range s.Fields {
		es := f.elemSize()
		if aligned && f.Aligned {
			off = align(off, scalarSize(f.Type))
		}
		off += es * f.count()
	}
	return off
}

// Encode serializes values (a JSON-ish map) into bytes. Missing fields
// encode as zero; constants always encode their constant.
func (st *Struct) Encode(rt *Runtime, sec *Section, values map[string]any) ([]byte, error) {
	var out []byte
	for _, f := range sec.Fields {
		if st.Aligned && f.Aligned {
			for len(out)%max(1, min(4, scalarSize(f.Type))) != 0 {
				out = append(out, 0)
			}
		}
		v, present := values[f.Name]
		if f.FromJSON != nil && present {
			conv, err := rt.applyTransform(f.FromJSON, v)
			if err != nil {
				return nil, fmt.Errorf("%s.%s from-json: %w", st.Name, f.Name, err)
			}
			v = conv
		}
		for i := 0; i < f.count(); i++ {
			ev := v
			if f.Repeat > 0 {
				arr, _ := v.([]any)
				ev = nil
				if i < len(arr) {
					ev = arr[i]
				}
			}
			b, err := f.encodeOne(rt, ev)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", st.Name, f.Name, err)
			}
			out = append(out, b...)
		}
	}
	return out, nil
}

func (f *Field) encodeOne(rt *Runtime, v any) ([]byte, error) {
	if f.IsConstant {
		return putUint(f.Type, int64(f.Constant), f.elemSize()), nil
	}
	switch {
	case scalarSize(f.Type) > 0:
		if f.Type == "float32" {
			b := make([]byte, 4)
			binary.LittleEndian.PutUint32(b, math.Float32bits(float32(toFloat(v))))
			return b, nil
		}
		n := toInt(v)
		if err := f.validate(n); err != nil {
			return nil, err
		}
		return putUint(f.Type, n, scalarSize(f.Type)), nil
	case f.Type == "bytearray":
		b := make([]byte, f.Size)
		copy(b, toBytes(v))
		return b, nil
	case f.Type == "varstring":
		s, _ := v.(string)
		return []byte(s), nil
	case f.nested != nil:
		m, _ := v.(map[string]any)
		return f.nested.Encode(rt, f.nested.Outgoing, m)
	}
	return nil, fmt.Errorf("unknown field type %q", f.Type)
}

func (f *Field) validate(n int64) error {
	if f.Range != nil && (n < f.Range[0] || n > f.Range[1]) {
		return fmt.Errorf("value %d outside range %d..%d", n, f.Range[0], f.Range[1])
	}
	if f.Excluding != nil && n >= f.Excluding[0] && n <= f.Excluding[1] {
		return fmt.Errorf("value %d in excluded range %d..%d", n, f.Excluding[0], f.Excluding[1])
	}
	if len(f.Values) > 0 {
		for _, ok := range f.Values {
			if ok == n {
				return nil
			}
		}
		return fmt.Errorf("value %d not one of %v", n, f.Values)
	}
	return nil
}

func putUint(t string, n int64, size int) []byte {
	b := make([]byte, size)
	u := uint64(n)
	for i := 0; i < size && i < 8; i++ {
		b[i] = byte(u >> (8 * i))
	}
	return b
}

// Decode parses bytes into a JSON-ish map using the given section.
func (st *Struct) Decode(rt *Runtime, sec *Section, data []byte) (map[string]any, int, error) {
	out := map[string]any{}
	off := 0
	for _, f := range sec.Fields {
		if st.Aligned && f.Aligned {
			off = align(off, scalarSize(f.Type))
		}
		var vals []any
		for i := 0; i < f.count(); i++ {
			v, n, err := f.decodeOne(rt, data, off)
			if err != nil {
				return out, off, fmt.Errorf("%s.%s: %w", st.Name, f.Name, err)
			}
			off += n
			vals = append(vals, v)
		}
		var v any
		if f.Repeat > 0 {
			v = vals
		} else if len(vals) > 0 {
			v = vals[0]
		}
		if f.ToJSON != nil {
			if conv, err := rt.applyTransform(f.ToJSON, v); err == nil {
				v = conv
			}
		}
		out[f.Name] = v
	}
	return out, off, nil
}

func (f *Field) decodeOne(rt *Runtime, data []byte, off int) (any, int, error) {
	get := func(n int) []byte {
		b := make([]byte, n)
		if off < len(data) {
			copy(b, data[off:])
		}
		return b
	}
	switch {
	case scalarSize(f.Type) > 0:
		n := scalarSize(f.Type)
		b := get(n)
		var u uint64
		for i := 0; i < n; i++ {
			u |= uint64(b[i]) << (8 * i)
		}
		switch f.Type {
		case "float32":
			return float64(math.Float32frombits(uint32(u))), n, nil
		case "int8":
			return int64(int8(u)), n, nil
		case "int16":
			return int64(int16(u)), n, nil
		case "int24":
			if u&0x800000 != 0 {
				return int64(u) - 0x1000000, n, nil
			}
			return int64(u), n, nil
		case "int32":
			return int64(int32(u)), n, nil
		}
		return int64(u), n, nil
	case f.Type == "bytearray":
		b := get(f.Size)
		arr := make([]any, len(b))
		for i, x := range b {
			arr[i] = int64(x)
		}
		return arr, f.Size, nil
	case f.Type == "varstring":
		if off >= len(data) {
			return "", 0, nil
		}
		rest := data[off:]
		end := len(rest)
		for i, c := range rest {
			if c == 0 {
				end = i
				break
			}
		}
		return string(rest[:end]), len(rest), nil
	case f.nested != nil:
		end := off + f.elemSize()
		if end > len(data) {
			end = len(data)
		}
		var chunk []byte
		if off < len(data) {
			chunk = data[off:end]
		}
		m, n, err := f.nested.Decode(rt, f.nested.Incoming, chunk)
		if n < f.elemSize() {
			n = f.elemSize()
		}
		return m, n, err
	}
	return nil, 0, fmt.Errorf("unknown field type %q", f.Type)
}

// Defaults returns the struct's JSON shape with constants filled in and
// everything else zero, which is what the engine uses as a starting point.
func (st *Struct) Defaults(rt *Runtime) map[string]any {
	m, _, _ := st.Decode(rt, st.Outgoing, nil)
	for _, f := range st.Outgoing.Fields {
		if f.IsConstant {
			m[f.Name] = int64(f.Constant)
		}
	}
	return m
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case float32:
		return int64(x)
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		n, _ := strconv.ParseInt(x, 0, 64)
		return n
	}
	return 0
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return 0
}

func toBytes(v any) []byte {
	switch x := v.(type) {
	case []byte:
		return x
	case string:
		return []byte(x)
	case []any:
		b := make([]byte, len(x))
		for i, e := range x {
			b[i] = byte(toInt(e))
		}
		return b
	}
	return nil
}
