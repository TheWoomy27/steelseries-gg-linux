// Package paths resolves the on-disk locations used by gg-linux.
//
// Everything that comes from the user's SteelSeries installer (the Electron
// client, device specifications, database migrations, images) is extracted
// into Data() by gg-setup and never shipped with this repository.
package paths

import (
	"os"
	"path/filepath"
)

// Data is the root data directory, $XDG_DATA_HOME/steelseries-gg.
func Data() string {
	if d := os.Getenv("GG_LINUX_DATA"); d != "" {
		return d
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "steelseries-gg")
}

// Extracted installer content.
func Client() string       { return filepath.Join(Data(), "client") }
func ClientAsar() string   { return filepath.Join(Client(), "app.asar") }
func EngineRoot() string   { return filepath.Join(Data(), "engine") }
func DeviceSpecs() string  { return filepath.Join(EngineRoot(), "deviceSpecifications") }
func Migrations() string   { return filepath.Join(EngineRoot(), "configurationMigrations") }
func EngineDBMig() string  { return filepath.Join(EngineRoot(), "db", "migrations") }
func GGDBMig() string      { return filepath.Join(Data(), "gg", "db", "migrations") }
func Localization() string { return filepath.Join(Data(), "gg", "localization") }
func Assets() string       { return filepath.Join(Data(), "assets") }
func Firmware() string     { return filepath.Join(EngineRoot(), "firmware") }
func PrismDBMig() string   { return filepath.Join(EngineRoot(), "prism", "db", "migrations") }

// Runtime state.
func State() string     { return filepath.Join(Data(), "state") }
func Logs() string      { return filepath.Join(Data(), "Logs") }
func CoreProps() string { return filepath.Join(Data(), "coreProps.json") }
func TLSDir() string    { return filepath.Join(State(), "tls") }
func EngineDB() string  { return filepath.Join(State(), "engine.db") }
func GGDB() string      { return filepath.Join(State(), "gg.db") }
func PrismDB() string   { return filepath.Join(State(), "prism.db") }

// GameSenseCoreProps is where GameSense SDK clients look for the engine
// address. On Windows this is %PROGRAMDATA%/SteelSeries/SteelSeries Engine 3;
// Linux has no equivalent, so we use the data dir and also export the path
// via $GAMESENSE_COREPROPS for clients that support it.
func GameSenseCoreProps() string {
	return filepath.Join(Data(), "SteelSeries Engine 3", "coreProps.json")
}

// EnsureRuntimeDirs creates the writable directories the daemon needs.
func EnsureRuntimeDirs() error {
	for _, d := range []string{State(), Logs(), TLSDir(), filepath.Dir(GameSenseCoreProps())} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
