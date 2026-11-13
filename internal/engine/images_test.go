package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeviceImage(t *testing.T) {
	data := t.TempDir()
	t.Setenv("GG_LINUX_DATA", data)
	dir := filepath.Join(data, "engine", "assets", "loadoutImages", "devices")
	os.MkdirAll(filepath.Join(dir, "generics"), 0o755)
	for _, n := range []string{"aerox_5", "alias", "alias_pro", "arctis_nova_7", "arctis_nova_7_gen2_black", "generics/keyboard",
		"apex_pro", "apex_pro_tkl_2024", "sensei_pro_white"} {
		os.WriteFile(filepath.Join(dir, n+".webp"), nil, 0o644)
	}
	for _, c := range []struct {
		names []string
		typ   int
		want  string
	}{
		{[]string{"", "aerox_5_wireless"}, 1, "devices/aerox_5.webp"},
		{[]string{"", "alias"}, 12, "devices/alias.webp"},
		{[]string{"arctis_nova_7_upgrade_tx", "arctis_nova_7_gen2_tx"}, 3, "devices/arctis_nova_7.webp"},
		{[]string{"arctis_nova_7_gen2_black", "arctis_nova_7_gen2_tx"}, 3, "devices/arctis_nova_7_gen2_black.webp"},
		// Windows' own table, where the name alone would mislead.
		{[]string{"", "apex_7"}, 0, "devices/apex_pro.webp"},
		{[]string{"", "apex_pro_tkl_wireless_2024"}, 0, "devices/apex_pro_tkl_2024.webp"},
		{[]string{"sensei_pro_mini_white", "sensei_rival_pro"}, 1, "devices/sensei_pro_white.webp"},
		{[]string{"", "arctis_nova_7_gen2_tx"}, 3, "devices/arctis_nova_7_gen2_black.webp"},
		// A table picture that isn't there: the name's own, then generic.
		{[]string{"", "apex_7_tkl"}, 0, "devices/generics/keyboard.webp"},
		{[]string{"", "rival_3"}, 1, ""}, // no mouse picture in this set
	} {
		if got := deviceImage(c.names, c.typ); got != c.want {
			t.Errorf("deviceImage(%v) = %q, want %q", c.names, got, c.want)
		}
	}
}
