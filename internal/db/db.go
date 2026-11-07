// Package db opens the Engine's SQLite database and applies the goose
// migrations shipped in the SteelSeries installer, so the schema and the
// seeded device catalog match the Windows Engine exactly.
package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type migration struct {
	version int64
	path    string
}

var versionRe = regexp.MustCompile(`^(\d+)_`)

// Open opens (creating if needed) the database at path and applies every
// pending migration found under migrationsDir/*/*.sql.
func Open(path, migrationsDir string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(off)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db, migrationsDir); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB, dir string) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT, version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL, tstamp TIMESTAMP DEFAULT (datetime('now')))`); err != nil {
		return err
	}
	applied := map[int64]bool{}
	rows, err := db.Query(`SELECT version_id FROM goose_db_version WHERE is_applied = 1`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int64
		rows.Scan(&v)
		applied[v] = true
	}
	rows.Close()

	files, _ := filepath.Glob(filepath.Join(dir, "*", "*.sql"))
	var migs []migration
	for _, f := range files {
		m := versionRe.FindStringSubmatch(filepath.Base(f))
		if m == nil {
			continue
		}
		v, _ := strconv.ParseInt(m[1], 10, 64)
		migs = append(migs, migration{v, f})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	start := time.Now()
	n := 0
	for _, m := range migs {
		if applied[m.version] {
			continue
		}
		if err := apply(db, m); err != nil {
			return fmt.Errorf("migration %s: %w", filepath.Base(m.path), err)
		}
		n++
	}
	if n > 0 {
		slog.Info("database migrated", "dir", dir, "applied", n, "ms", time.Since(start).Milliseconds())
	}
	return nil
}

// upSection extracts the "-- +goose Up" part of a goose migration.
func upSection(src string) string {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	var out strings.Builder
	inUp := false
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "-- +goose Up"):
			inUp = true
			continue
		case strings.HasPrefix(t, "-- +goose Down"):
			inUp = false
			continue
		case strings.HasPrefix(t, "-- +goose"):
			continue
		}
		if inUp {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func apply(db *sql.DB, m migration) error {
	b, err := os.ReadFile(m.path)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(upSection(string(b))); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, m.version); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
