package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/arch/x86/x86asm"
)

// Go 1.22-1.24 runtime type layout (amd64): abi.Type is 48 bytes; struct,
// pointer, slice and map types extend it.
const (
	kindArray  = 17
	kindChan   = 18
	kindMap    = 21
	kindPtr    = 22
	kindSlice  = 23
	kindStruct = 25

	// moduledata field offsets (Go 1.24)
	mdTypes     = 296
	mdETypes    = 304
	mdTypelinks = 352
)

var kindNames = map[uint8]string{1: "bool", 2: "int", 3: "int8", 4: "int16", 5: "int32", 6: "int64", 7: "uint",
	8: "uint8", 9: "uint16", 10: "uint32", 11: "uint64", 12: "uintptr", 13: "float32", 14: "float64",
	15: "complex64", 16: "complex128", 24: "string", 26: "unsafe.Pointer"}

type typeWalker struct {
	b          *bin
	types, end uint64
	seen       map[uint64]bool
	names      map[uint64]string
	out        map[string]string
}

func (b *bin) u64(a uint64) uint64 {
	if d := b.read(a, 8); len(d) == 8 {
		return binary.LittleEndian.Uint64(d)
	}
	return 0
}

func (b *bin) u32(a uint64) uint32 {
	if d := b.read(a, 4); len(d) == 4 {
		return binary.LittleEndian.Uint32(d)
	}
	return 0
}

func varint(d []byte) (v, n int) {
	for i := 0; i < len(d) && i < 5; i++ {
		v |= int(d[i]&0x7f) << (7 * i)
		n++
		if d[i]&0x80 == 0 {
			break
		}
	}
	return
}

// name decodes an abi.Name: flags byte, varint length, bytes, then an
// optional varint-prefixed tag.
func (w *typeWalker) name(a uint64) (string, string) {
	d := w.b.read(a, 4096)
	if len(d) < 2 {
		return "", ""
	}
	l, n := varint(d[1:])
	off := 1 + n
	if off+l > len(d) {
		return "", ""
	}
	nm, tag := string(d[off:off+l]), ""
	off += l
	if d[0]&2 != 0 && off < len(d) {
		tl, n2 := varint(d[off:])
		if off+n2+tl <= len(d) {
			tag = string(d[off+n2 : off+n2+tl])
		}
	}
	return nm, tag
}

func (w *typeWalker) typeName(a uint64) string {
	if a == 0 {
		return "?"
	}
	if s, ok := w.names[a]; ok {
		return s
	}
	hdr := w.b.read(a, 48)
	if len(hdr) < 48 {
		return "?"
	}
	nm, _ := w.name(w.types + uint64(int64(int32(binary.LittleEndian.Uint32(hdr[40:])))))
	if hdr[20]&2 != 0 && nm != "" { // TFlagExtraStar
		nm = nm[1:]
	}
	if nm == "" {
		switch hdr[23] & 0x1f {
		case kindPtr:
			nm = "*" + w.typeName(w.b.u64(a+48))
		case kindSlice:
			nm = "[]" + w.typeName(w.b.u64(a+48))
		default:
			nm = kindNames[hdr[23]&0x1f]
		}
	}
	w.names[a] = nm
	return nm
}

func (w *typeWalker) walk(a uint64, depth int) {
	if a < w.types || a >= w.end || w.seen[a] || depth > 40 {
		return
	}
	w.seen[a] = true
	k := w.b.read(a+23, 1)
	if len(k) == 0 {
		return
	}
	switch k[0] & 0x1f {
	case kindPtr, kindSlice, kindChan, kindArray:
		w.walk(w.b.u64(a+48), depth+1)
	case kindMap:
		w.walk(w.b.u64(a+48), depth+1)
		w.walk(w.b.u64(a+56), depth+1)
	case kindStruct:
		fields, n := w.b.u64(a+56), w.b.u64(a+64)
		if n > 500 {
			return
		}
		var sb strings.Builder
		name := w.typeName(a)
		fmt.Fprintf(&sb, "type %s struct {\n", name)
		for i := uint64(0); i < n; i++ {
			f := fields + i*24
			fn, tag := w.name(w.b.u64(f))
			ft := w.b.u64(f + 8)
			if tag != "" {
				tag = " `" + tag + "`"
			}
			fmt.Fprintf(&sb, "\t%-28s %-40s //+%d%s\n", fn, w.typeName(ft), w.b.u64(f+16), tag)
			w.walk(ft, depth+1)
		}
		sb.WriteString("}\n")
		if name != "" && name != "?" {
			w.out[name] = sb.String()
		}
	}
}

// dumpTypes finds moduledata (the word pointing at the pclntab), then walks
// every type reachable from typelinks and from LEAs in code.
func (b *bin) dumpTypes(filter *regexp.Regexp) {
	var md uint64
	for _, s := range b.sections {
		for i := 0; i+8 <= len(s.data); i += 8 {
			if binary.LittleEndian.Uint64(s.data[i:]) != b.pclnAddr {
				continue
			}
			cand := s.addr + uint64(i)
			if t, e := b.u64(cand+mdTypes), b.u64(cand+mdETypes); t != 0 && e > t && e-t < 1<<28 {
				md = cand
			}
		}
	}
	if md == 0 {
		fmt.Fprintln(os.Stderr, "moduledata not found")
		return
	}
	w := &typeWalker{b: b, types: b.u64(md + mdTypes), end: b.u64(md + mdETypes),
		seen: map[uint64]bool{}, names: map[uint64]string{}, out: map[string]string{}}
	tl, n := b.u64(md+mdTypelinks), b.u64(md+mdTypelinks+8)
	var roots []uint64
	for i := uint64(0); i < n; i++ {
		roots = append(roots, w.types+uint64(int64(int32(b.u32(tl+i*4)))))
	}
	for code, pc := b.text, b.textAddr; len(code) > 0; {
		inst, err := x86asm.Decode(code, 64)
		if err != nil || inst.Len == 0 {
			code, pc = code[1:], pc+1
			continue
		}
		if inst.Op == x86asm.LEA {
			if m, ok := inst.Args[1].(x86asm.Mem); ok && m.Base == x86asm.RIP {
				if t := uint64(int64(pc) + int64(inst.Len) + m.Disp); t >= w.types && t < w.end {
					roots = append(roots, t)
				}
			}
		}
		code, pc = code[inst.Len:], pc+uint64(inst.Len)
	}
	for _, r := range roots {
		w.walk(r, 0)
	}
	var keys []string
	for k := range w.out {
		if filter.MatchString(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Print(w.out[k])
	}
}
