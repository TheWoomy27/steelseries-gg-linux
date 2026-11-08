package sonar

import (
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"ggl/internal/db"
)

// TestUpgradeMatchesSonar upgrades every config shipped in the installer's
// Sonar migrations and compares the result with Sonar's own upgraders, as
// recorded by tools/sonar-oracle. It needs GG_LINUX_DATA (an imported
// installer). GG_TEST_SONAR_EXPORT writes the configs the oracle takes;
// GG_TEST_SONAR_ORACLE is the oracle's output to compare with.
func TestUpgradeMatchesSonar(t *testing.T) {
	data, export, oracle := os.Getenv("GG_LINUX_DATA"), os.Getenv("GG_TEST_SONAR_EXPORT"), os.Getenv("GG_TEST_SONAR_ORACLE")
	if data == "" || export == "" && oracle == "" {
		t.Skip("set GG_LINUX_DATA and GG_TEST_SONAR_EXPORT or GG_TEST_SONAR_ORACLE")
	}
	d, err := db.Open(t.TempDir()+"/sonar.db", data+"/sonar/db-migrations")
	if err != nil {
		t.Fatal(err)
	}
	if export != "" {
		exportConfigs(t, d, export)
		if oracle == "" {
			return
		}
	}
	b, err := os.ReadFile(oracle)
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Configs []struct {
			ID          string `json:"id"`
			To          int    `json:"to"`
			Image       string `json:"image"`
			Data        any    `json:"data"`
			DefaultData any    `json:"defaultData"`
			Error       string `json:"error"`
		} `json:"configs"`
		Generic map[string]any `json:"generic"`
	}
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	s := &Service{db: d}
	all, err := s.queryConfigs(``)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*config{}
	for _, c := range all {
		byID[c.ID] = c
	}
	same := func(a, b any) bool { return reflect.DeepEqual(normalize(t, a), normalize(t, b)) }
	checked := 0
	for _, w := range want.Configs {
		c := byID[w.ID]
		if c == nil || w.Error != "" {
			continue
		}
		if err := upgrade(c); err != nil {
			t.Errorf("%s (%s): %v", c.Name, w.ID, err)
			continue
		}
		if c.SchemaVersion != w.To || c.Image != w.Image && w.Image != "" {
			t.Errorf("%s: schema %d image %q, want %d %q", c.Name, c.SchemaVersion, c.Image, w.To, w.Image)
		}
		if !same(c.Data, w.Data) {
			t.Errorf("%s (%s): data differs\n got %s\nwant %s", c.Name, w.ID, jsonText(c.Data), jsonText(w.Data))
		}
		if !same(c.DefaultData, w.DefaultData) {
			t.Errorf("%s (%s): default data differs", c.Name, w.ID)
		}
		checked++
	}
	for vad, g := range want.Generic {
		var v int
		json.Unmarshal([]byte(vad), &v)
		if !same(defaultData(v), g) {
			t.Errorf("default data for %s differs\n got %s\nwant %s", vadNames[v], jsonText(defaultData(v)), jsonText(g))
		}
	}
	t.Logf("%d configs match Sonar's upgrades", checked)
}

// exportConfigs writes the stored configs as tools/sonar-oracle reads them.
func exportConfigs(t *testing.T, d *sql.DB, path string) {
	rows, err := d.Query(`SELECT id, name, vad, schema_version, is_preset, image, data, default_data FROM configs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, name, image, data, defData string
		var vad, schema, preset int
		if err := rows.Scan(&id, &name, &vad, &schema, &preset, &image, &data, &defData); err != nil {
			t.Fatal(err)
		}
		out = append(out, map[string]any{"id": id, "name": name, "vad": vad, "schema_version": schema,
			"is_preset": preset, "image": image, "data": data, "default_data": defData})
	}
	if err := os.WriteFile(path, []byte(jsonText(out)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// normalize round-trips a value through plain JSON so numbers and objects
// compare by value.
func normalize(t *testing.T, v any) any {
	var out any
	if err := json.Unmarshal([]byte(jsonText(v)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
