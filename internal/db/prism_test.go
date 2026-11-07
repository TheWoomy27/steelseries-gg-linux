package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigratePrismDB(t *testing.T) {
	data := os.Getenv("GG_LINUX_DATA")
	if data == "" {
		t.Skip("GG_LINUX_DATA not set")
	}
	out := filepath.Join(t.TempDir(), "prism.db")
	if p := os.Getenv("GG_LINUX_DB_OUT"); p != "" {
		out = p
	}
	db, err := Open(out, filepath.Join(data, "engine", "prism", "db", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
}
