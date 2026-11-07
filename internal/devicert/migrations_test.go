package devicert

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigrateCatalogDefaults runs every device's configuration migrations
// over its catalog default settings. It needs an Engine database created by
// ggd; point GG_LINUX_DATA at the data directory.
func TestMigrateCatalogDefaults(t *testing.T) {
	data := os.Getenv("GG_LINUX_DATA")
	if data == "" {
		t.Skip("GG_LINUX_DATA not set")
	}
	rt, err := New(filepath.Join(data, "engine", "deviceSpecifications"), filepath.Join(data, "engine"), nullTransport{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.LoadMigrations(filepath.Join(data, "engine", "configurationMigrations")); err != nil {
		t.Fatal(err)
	}
	for _, e := range rt.LoadErrors {
		t.Errorf("load: %s", e)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(data, "state", "engine.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name, coalesce(settings,'{}'), coalesce(undeployedsettings,'{}') FROM devices`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	migrated := 0
	for rows.Next() {
		var name, settings, undeployed string
		rows.Scan(&name, &settings, &undeployed)
		s, _, changed, err := rt.MigrateSettings(name, settings, undeployed)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !changed {
			continue
		}
		migrated++
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil || m["schema"] == nil {
			t.Errorf("%s: migrated settings unusable: %v", name, err)
		}
	}
	t.Logf("%d devices migrated", migrated)
}
