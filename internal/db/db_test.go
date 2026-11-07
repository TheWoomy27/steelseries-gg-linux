package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateEngineDB(t *testing.T) {
	data := os.Getenv("GG_LINUX_DATA")
	if data == "" {
		t.Skip("GG_LINUX_DATA not set")
	}
	out := filepath.Join(t.TempDir(), "engine.db")
	if p := os.Getenv("GG_LINUX_DB_OUT"); p != "" {
		out = p
	}
	db, err := Open(out, filepath.Join(data, "engine", "db", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM devices`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	t.Logf("devices in catalog: %d", n)
}
