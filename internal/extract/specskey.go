package extract

import (
	"bytes"
	"debug/gosym"
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
)

// The engine's device specifications (*.edevice) are OpenPGP symmetric
// messages. The passphrase is assembled in
// fileencryption.DecryptDeviceFile.func1 from two string literals:
//
//	lea rax, [rip+part1]
//	mov ebx, len
//	lea rcx, [rip+part2]
//	mov rdi, rbx
//	call runtime.concatbyte2
//
// Rather than carrying the passphrase in this repository, we recover it from
// the user's own copy of SteelSeriesEngine.exe using the Go pclntab.
const passphraseFunc = "github.com/steelseries/engine/pkg/utils/fileencryption.DecryptDeviceFile.func1"

type peImage struct {
	sections []peSection
}

type peSection struct {
	addr uint64
	data []byte
}

func (p *peImage) read(addr uint64, n int) []byte {
	for _, s := range p.sections {
		if addr >= s.addr && addr+uint64(n) <= s.addr+uint64(len(s.data)) {
			off := addr - s.addr
			return s.data[off : off+uint64(n)]
		}
	}
	return nil
}

// SpecPassphrase extracts the device-specification passphrase from a
// SteelSeriesEngine.exe binary.
func SpecPassphrase(enginePath string) ([]byte, error) {
	f, err := pe.Open(enginePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	oh, ok := f.OptionalHeader.(*pe.OptionalHeader64)
	if !ok {
		return nil, errors.New("engine binary is not PE32+")
	}
	img := &peImage{}
	for _, s := range f.Sections {
		d, err := s.Data()
		if err != nil {
			continue
		}
		img.sections = append(img.sections, peSection{oh.ImageBase + uint64(s.VirtualAddress), d})
	}

	table, err := findPclntab(img)
	if err != nil {
		return nil, err
	}
	fn := table.LookupFunc(passphraseFunc)
	if fn == nil {
		return nil, fmt.Errorf("%s not found in engine binary", passphraseFunc)
	}
	code := img.read(fn.Entry, int(fn.End-fn.Entry))
	if code == nil {
		return nil, errors.New("passphrase function body unreadable")
	}

	// Collect RIP-relative LEAs (48 8d /r with mod=00 rm=101) and the
	// first "mov ebx, imm32" (bb imm32) which carries the part length.
	var targets []uint64
	length := -1
	for i := 0; i+7 <= len(code); i++ {
		if (code[i] == 0x48 || code[i] == 0x4c) && code[i+1] == 0x8d && code[i+2]&0xc7 == 0x05 {
			rel := int32(binary.LittleEndian.Uint32(code[i+3:]))
			targets = append(targets, uint64(int64(fn.Entry)+int64(i)+7+int64(rel)))
			i += 6
			continue
		}
		if code[i] == 0xbb && length < 0 && i+5 <= len(code) {
			length = int(binary.LittleEndian.Uint32(code[i+1:]))
		}
	}
	if len(targets) < 2 || length <= 0 || length > 4096 {
		return nil, fmt.Errorf("unexpected passphrase function layout (leas=%d len=%d)", len(targets), length)
	}
	a := img.read(targets[0], length)
	b := img.read(targets[1], length)
	if a == nil || b == nil {
		return nil, errors.New("passphrase literals out of range")
	}
	return append(append([]byte{}, a...), b...), nil
}

// findPclntab locates the Go 1.20+ pclntab by its magic and parses it.
func findPclntab(img *peImage) (*gosym.Table, error) {
	magic := []byte{0xf1, 0xff, 0xff, 0xff, 0x00, 0x00, 0x01, 0x08}
	for _, s := range img.sections {
		off := 0
		for {
			i := bytes.Index(s.data[off:], magic)
			if i < 0 {
				break
			}
			cand := s.data[off+i:]
			if len(cand) > 32 {
				textStart := binary.LittleEndian.Uint64(cand[24:])
				t, err := gosym.NewTable(nil, gosym.NewLineTable(cand, textStart))
				if err == nil && len(t.Funcs) > 1000 {
					return t, nil
				}
			}
			off += i + 1
		}
	}
	return nil, errors.New("Go pclntab not found in engine binary")
}
