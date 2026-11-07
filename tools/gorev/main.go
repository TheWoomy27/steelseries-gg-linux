// gorev inspects stripped Go binaries (Windows PE or macOS Mach-O) using the
// pclntab and runtime type metadata Go always embeds. It is how the GG core,
// Engine and PrismSync APIs were mapped, and how they can be re-checked when
// SteelSeries ships a new version.
//
//	gorev funcs  BIN [REGEX]   list functions with entry, size and source line
//	gorev dis    BIN REGEX     disassemble matching functions, resolving
//	                           calls, string literals and func values
//	gorev xref   BIN REGEX     list functions that call matching functions
//	gorev str    BIN ADDR LEN  dump bytes at a virtual address
//	gorev types  BIN [REGEX]   print struct types with field offsets and tags
package main

import (
	"bytes"
	"debug/gosym"
	"debug/macho"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/arch/x86/x86asm"
)

type section struct {
	addr uint64
	data []byte
}

type bin struct {
	sections []section
	textAddr uint64
	text     []byte
	pclnAddr uint64
	table    *gosym.Table
	byEntry  map[uint64]*gosym.Func
}

func load(path string) (*bin, error) {
	b := &bin{byEntry: map[uint64]*gosym.Func{}}
	if f, err := pe.Open(path); err == nil {
		var base uint64
		switch oh := f.OptionalHeader.(type) {
		case *pe.OptionalHeader64:
			base = oh.ImageBase
		case *pe.OptionalHeader32:
			base = uint64(oh.ImageBase)
		}
		for _, s := range f.Sections {
			d, _ := s.Data()
			b.sections = append(b.sections, section{base + uint64(s.VirtualAddress), d})
			if s.Name == ".text" {
				b.textAddr, b.text = base+uint64(s.VirtualAddress), d
			}
		}
	} else if f, err := macho.Open(path); err == nil {
		for _, s := range f.Sections {
			d, _ := s.Data()
			b.sections = append(b.sections, section{s.Addr, d})
			if s.Name == "__text" {
				b.textAddr, b.text = s.Addr, d
			}
		}
	} else {
		return nil, fmt.Errorf("%s: not a PE or Mach-O binary", path)
	}
	// Go 1.20+ pclntab: magic, two pad bytes, minLC=1, ptrSize=8; the
	// header records the real text start (it differs from the section
	// start in externally linked binaries).
	magic := []byte{0xf1, 0xff, 0xff, 0xff, 0x00, 0x00, 0x01, 0x08}
	for _, s := range b.sections {
		for off := 0; ; {
			i := bytes.Index(s.data[off:], magic)
			if i < 0 {
				break
			}
			cand := s.data[off+i:]
			if len(cand) > 32 {
				ts := binary.LittleEndian.Uint64(cand[24:])
				t, err := gosym.NewTable(nil, gosym.NewLineTable(cand, ts))
				if err == nil && len(t.Funcs) > 1000 {
					b.table, b.pclnAddr = t, s.addr+uint64(off+i)
					for i := range t.Funcs {
						b.byEntry[t.Funcs[i].Entry] = &t.Funcs[i]
					}
					return b, nil
				}
			}
			off += i + 1
		}
	}
	return nil, fmt.Errorf("%s: Go pclntab not found", path)
}

func (b *bin) read(addr uint64, n int) []byte {
	for _, s := range b.sections {
		if addr >= s.addr && addr < s.addr+uint64(len(s.data)) {
			o := addr - s.addr
			e := min(o+uint64(n), uint64(len(s.data)))
			return s.data[o:e]
		}
	}
	return nil
}

func printableRun(bs []byte) string {
	end := 0
	for end < len(bs) && bs[end] >= 0x20 && bs[end] < 0x7f {
		end++
	}
	return string(bs[:end])
}

func (b *bin) dis(fn *gosym.Func) {
	file, line, _ := b.table.PCToLine(fn.Entry)
	fmt.Printf("\n==== %s  @%#x-%#x  (%s:%d)\n", fn.Name, fn.Entry, fn.End, file, line)
	code := b.read(fn.Entry, int(fn.End-fn.Entry))
	pc := fn.Entry
	var lastLea uint64
	lastLine := -1
	for len(code) > 0 {
		inst, err := x86asm.Decode(code, 64)
		if err != nil || inst.Len == 0 {
			code, pc = code[1:], pc+1
			continue
		}
		comment := ""
		if _, ln, _ := b.table.PCToLine(pc); ln != lastLine {
			comment, lastLine = fmt.Sprintf(" ; L%d", ln), ln
		}
		for _, a := range inst.Args {
			switch v := a.(type) {
			case x86asm.Rel:
				if f := b.table.PCToFunc(uint64(int64(pc) + int64(inst.Len) + int64(v))); f != nil {
					comment += " -> " + f.Name
				}
			case x86asm.Mem:
				if v.Base != x86asm.RIP {
					continue
				}
				tgt := uint64(int64(pc) + int64(inst.Len) + v.Disp)
				if inst.Op != x86asm.LEA {
					comment += fmt.Sprintf(" [%#x]", tgt)
					continue
				}
				lastLea = tgt
				if f := b.byEntry[tgt]; f != nil {
					comment += " &" + f.Name
				} else if pv := b.read(tgt, 8); len(pv) == 8 && b.byEntry[binary.LittleEndian.Uint64(pv)] != nil {
					comment += " &" + b.byEntry[binary.LittleEndian.Uint64(pv)].Name
				} else if s := printableRun(b.read(tgt, 64)); len(s) >= 2 {
					comment += fmt.Sprintf(" str@%#x %q", tgt, s)
				} else {
					comment += fmt.Sprintf(" @%#x", tgt)
				}
			case x86asm.Imm:
				if inst.Op == x86asm.MOV && lastLea != 0 && v > 0 && v < 4096 {
					if bs := b.read(lastLea, int(v)); printableRun(bs) == string(bs) {
						comment += fmt.Sprintf(" len=%d %q", v, string(bs))
					}
					lastLea = 0
				}
			}
		}
		fmt.Printf("  %x: %-50s%s\n", pc, x86asm.IntelSyntax(inst, pc, nil), comment)
		code, pc = code[inst.Len:], pc+uint64(inst.Len)
	}
}

func (b *bin) xref(re *regexp.Regexp) {
	for i := range b.table.Funcs {
		fn := &b.table.Funcs[i]
		code := b.read(fn.Entry, int(fn.End-fn.Entry))
		pc := fn.Entry
		hits := map[string]bool{}
		for len(code) > 0 {
			inst, err := x86asm.Decode(code, 64)
			if err != nil || inst.Len == 0 {
				code, pc = code[1:], pc+1
				continue
			}
			if inst.Op == x86asm.CALL {
				if r, ok := inst.Args[0].(x86asm.Rel); ok {
					if f := b.table.PCToFunc(uint64(int64(pc) + int64(inst.Len) + int64(r))); f != nil && re.MatchString(f.Name) {
						hits[f.Name] = true
					}
				}
			}
			code, pc = code[inst.Len:], pc+uint64(inst.Len)
		}
		for h := range hits {
			fmt.Printf("%s -> %s\n", fn.Name, h)
		}
	}
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: gorev funcs|dis|xref|str|types BIN ...")
		os.Exit(2)
	}
	b, err := load(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	arg := func(i int, def string) string {
		if len(os.Args) > i {
			return os.Args[i]
		}
		return def
	}
	switch os.Args[1] {
	case "funcs":
		re := regexp.MustCompile(arg(3, "."))
		for _, f := range b.table.Funcs {
			if re.MatchString(f.Name) {
				file, line, _ := b.table.PCToLine(f.Entry)
				fmt.Printf("%#x %6d %s  %s:%d\n", f.Entry, f.End-f.Entry, f.Name, file, line)
			}
		}
	case "dis":
		re := regexp.MustCompile(arg(3, "^$"))
		for i := range b.table.Funcs {
			if re.MatchString(b.table.Funcs[i].Name) {
				b.dis(&b.table.Funcs[i])
			}
		}
	case "xref":
		b.xref(regexp.MustCompile(arg(3, "^$")))
	case "str":
		a, _ := strconv.ParseUint(strings.TrimPrefix(arg(3, "0"), "0x"), 16, 64)
		n, _ := strconv.Atoi(arg(4, "64"))
		os.Stdout.Write(b.read(a, n))
	case "types":
		b.dumpTypes(regexp.MustCompile(arg(3, ".")))
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}
