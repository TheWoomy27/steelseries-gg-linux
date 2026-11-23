// gg-check checks a SteelSeries GG import (gg-setup's data directory)
// against what ggd needs. check.sh runs it; see there.
//
//	gg-check specs DATA      load every device declaration and the
//	                         configuration migrations
//	gg-check databases DATA  apply the Engine, Prism and Sonar database
//	                         migrations to new databases
package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"ggl/internal/db"
	"ggl/internal/devicert"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: gg-check specs|databases DATA")
		os.Exit(2)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	data := os.Args[2]
	var ok bool
	switch os.Args[1] {
	case "specs":
		ok = specs(data)
	case "databases":
		ok = databases(data)
	default:
		fmt.Fprintln(os.Stderr, "usage: gg-check specs|databases DATA")
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

type noTransport struct{}

func (noTransport) Chunk(devicert.Target, int, int, []byte, bool, time.Duration, time.Duration) ([]byte, error) {
	return nil, fmt.Errorf("no device")
}
func (noTransport) Connected(devicert.Target) bool { return false }

func specs(data string) bool {
	engine := filepath.Join(data, "engine")
	rt, err := devicert.New(filepath.Join(engine, "deviceSpecifications"), engine, noTransport{}, nil)
	if err != nil {
		fmt.Println("device specifications: cannot index:", err)
		return false
	}
	ids := rt.KnownIDs()
	failed := 0
	for _, id := range ids {
		if _, err := rt.Device(id); err != nil {
			failed++
			fmt.Printf("  %#x: %v\n", id, err)
		}
	}
	migrations := "configuration migrations load"
	if err := rt.LoadMigrations(filepath.Join(engine, "configurationMigrations")); err != nil {
		fmt.Println("configuration migrations:", err)
		migrations = "configuration migrations fail"
		failed++
	}
	fmt.Printf("%d device declarations, %d failed to load; %s\n", len(ids), failed, migrations)
	return failed == 0 && len(ids) > 0
}

func databases(data string) bool {
	tmp, err := os.MkdirTemp("", "gg-check-")
	if err != nil {
		fmt.Println(err)
		return false
	}
	defer os.RemoveAll(tmp)
	ok := true
	for _, d := range []struct{ name, dir string }{
		{"engine", "engine/db/migrations"},
		{"prism", "engine/prism/db/migrations"},
		{"sonar", "sonar/db-migrations"},
	} {
		sqlDB, err := db.Open(filepath.Join(tmp, d.name+".db"), filepath.Join(data, d.dir))
		if err != nil {
			fmt.Printf("%s database: %v\n", d.name, err)
			ok = false
			continue
		}
		var tables int
		sqlDB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables)
		sqlDB.Close()
		fmt.Printf("%s database: migrations apply (%d tables)\n", d.name, tables)
	}
	if ok {
		fmt.Println("Engine, Prism and Sonar migrations apply")
	}
	return ok
}
