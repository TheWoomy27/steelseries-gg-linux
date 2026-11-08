package sonar

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatMixCurves(t *testing.T) {
	for _, c := range []struct{ balance, scaled float64 }{
		{0, 0}, {1, 1}, {-1, -1}, {0.5, 2.0 / 3}, {-0.5, -2.0 / 3}, {0.6, 0.75},
	} {
		if got := scaledBalance(c.balance); math.Abs(got-c.scaled) > 1e-9 {
			t.Errorf("scaledBalance(%g) = %g, want %g", c.balance, got, c.scaled)
		}
	}
	for _, c := range []struct {
		game, chat int
		balance    float64
		ok         bool
	}{
		{100, 100, 0, true}, {40, 100, 0.6, true}, {100, 30, -0.7, true}, {0, 100, 1, true},
		{50, 50, 0, false}, {101, 100, 0, false},
	} {
		b, ok := dialBalance(c.game, c.chat)
		if ok != c.ok || math.Abs(b-c.balance) > 1e-9 {
			t.Errorf("dialBalance(%d, %d) = %g, %v; want %g, %v", c.game, c.chat, b, ok, c.balance, c.ok)
		}
	}
}

func TestVorbisChannels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.ogg")
	// The start of an Ogg page carrying a Vorbis identification header:
	// version 0, 8 channels, 48 kHz.
	header := append([]byte("OggS\x00\x02"+strings.Repeat("\x00", 20)+"\x01\x1e\x01vorbis\x00\x00\x00\x00\x08"), 0x80, 0xbb, 0, 0)
	if err := os.WriteFile(path, header, 0o644); err != nil {
		t.Fatal(err)
	}
	if n := vorbisChannels(path); n != 8 || vorbisChannelMaps[n] != "FL,FC,FR,SL,SR,RL,RR,LFE" {
		t.Errorf("vorbisChannels = %d", n)
	}
	if n := vorbisChannels(filepath.Join(t.TempDir(), "missing.ogg")); n != 0 {
		t.Errorf("missing file: %d channels", n)
	}
}
