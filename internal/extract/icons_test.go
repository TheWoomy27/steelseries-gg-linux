package extract

import (
	"encoding/binary"
	"image/color"
	"testing"
)

func TestDecodeIconDIB(t *testing.T) {
	// A 2x2 32-bit icon: header height counts the AND mask too, rows are
	// stored bottom-up and pixels as BGRA.
	b := make([]byte, 40, 40+2*2*4)
	binary.LittleEndian.PutUint32(b[0:], 40)
	binary.LittleEndian.PutUint32(b[4:], 2)
	binary.LittleEndian.PutUint32(b[8:], 4)
	binary.LittleEndian.PutUint16(b[12:], 1)
	binary.LittleEndian.PutUint16(b[14:], 32)
	b = append(b,
		0, 0, 255, 255, 0, 255, 0, 128, // bottom row: red, half-transparent green
		255, 0, 0, 255, 0, 0, 0, 0, // top row: blue, transparent
	)
	img, err := decodeIconDIB(b)
	if err != nil {
		t.Fatal(err)
	}
	want := map[[2]int]color.NRGBA{
		{0, 0}: {0, 0, 255, 255},
		{1, 0}: {0, 0, 0, 0},
		{0, 1}: {255, 0, 0, 255},
		{1, 1}: {0, 255, 0, 128},
	}
	for p, c := range want {
		if got := img.At(p[0], p[1]); got != c {
			t.Errorf("pixel %v = %v, want %v", p, got, c)
		}
	}
}

func TestGroupIconIDs(t *testing.T) {
	// GRPICONDIR with two entries (ids 6 and 10).
	b := []byte{0, 0, 1, 0, 2, 0}
	for _, id := range []uint16{6, 10} {
		e := make([]byte, 14)
		binary.LittleEndian.PutUint16(e[12:], id)
		b = append(b, e...)
	}
	ids, err := groupIconIDs(b)
	if err != nil || len(ids) != 2 || ids[0] != 6 || ids[1] != 10 {
		t.Fatalf("ids %v, err %v", ids, err)
	}
}
