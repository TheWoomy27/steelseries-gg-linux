package engine

import "testing"

// A device that reports its colour (colorSpinUpdate) shows as that
// variant; a variant the user picked stays unless the device forces it.
func TestOptimizeForChild(t *testing.T) {
	e := appsEngine(t)
	for _, q := range []string{
		`CREATE TABLE child_devices(id INTEGER PRIMARY KEY, parent_device_id INTEGER, name TEXT, full_name TEXT, is_default INTEGER, offsets TEXT, is_cmf INTEGER)`,
		`CREATE TABLE users_devices(user_id INTEGER, device_id INTEGER, hidden INTEGER DEFAULT 0, optimized_for INTEGER DEFAULT 0, first_plugin INTEGER DEFAULT 1, first_connection_time TEXT DEFAULT '')`,
		`INSERT INTO child_devices VALUES (11, 7, 'sensei_pro_black', 'Sensei Pro', 1, '', 1), (12, 7, 'sensei_pro_white', 'Sensei Pro White', 0, '', 1)`,
	} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	l := &liveDevice{dbID: 7, cat: &catalogDevice{ID: 7, Name: "sensei_rival_pro"}}
	if !e.optimizeForChild(l, "sensei_pro_white", false) || e.userDevice(7, false).OptimizedFor != 12 {
		t.Fatalf("not optimized for the white one: %+v", e.userDevice(7, false))
	}
	if e.optimizeForChild(l, "sensei_pro_black", false) || e.userDevice(7, false).OptimizedFor != 12 {
		t.Error("an unforced report replaced the variant")
	}
	if !e.optimizeForChild(l, "sensei_pro_black", true) || e.userDevice(7, false).OptimizedFor != 11 {
		t.Error("a forced report did not replace the variant")
	}
	if e.optimizeForChild(l, "rival_pro_black", true) {
		t.Error("a variant the device doesn't have")
	}
}
