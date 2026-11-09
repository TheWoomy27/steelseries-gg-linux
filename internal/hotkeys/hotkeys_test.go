package hotkeys

import "testing"

func TestSpec(t *testing.T) {
	for _, c := range []struct {
		mods, hid int
		want      string
	}{
		{4, 22, "ALT,S"},           // Moments clip
		{4 | 2, 16, "SHIFT ALT,M"}, // with Shift
		{0x40, 67, "ALT,F10"},      // right Alt
		{1 | 8, 39, "CTRL SUPER,0"},
		{0, 75, ",Prior"},
	} {
		if got, ok := Spec(c.mods, c.hid); !ok || got != c.want {
			t.Errorf("Spec(%d, %d) = %q, %v; want %q", c.mods, c.hid, got, ok, c.want)
		}
	}
	if _, ok := Spec(4, 0); ok {
		t.Error("named a key for HID usage 0")
	}
	if got := luaKeys("SHIFT ALT,M"); got != "SHIFT + ALT + M" {
		t.Errorf("luaKeys = %q", got)
	}
	if got := luaKeys(",F10"); got != "F10" {
		t.Errorf("luaKeys = %q", got)
	}
	if normalize("SHIFT ALT,M") != "9,m" {
		t.Errorf("normalize = %q", normalize("SHIFT ALT,M"))
	}
}
