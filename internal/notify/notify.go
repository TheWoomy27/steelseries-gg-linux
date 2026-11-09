// Package notify shows desktop notifications through the freedesktop
// notification service, where Windows GG shows toasts and overlays.
package notify

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/godbus/dbus/v5"

	"ggl/internal/paths"
)

const (
	service = "org.freedesktop.Notifications"
	path    = "/org/freedesktop/Notifications"
)

var (
	mu   sync.Mutex
	conn *dbus.Conn
	ids  = map[string]uint32{} // the last notification for each tag
)

// icon is GG's icon: the one install.sh puts in the icon theme, else the
// imported tray icon.
func icon() string {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, _ := os.UserHomeDir()
		data = filepath.Join(home, ".local", "share")
	}
	if _, err := os.Stat(filepath.Join(data, "icons", "hicolor", "256x256", "apps", "steelseries-gg.png")); err == nil {
		return "steelseries-gg"
	}
	if f := filepath.Join(paths.TrayIcons(), "ok-256.png"); fileExists(f) {
		return f
	}
	return ""
}

func fileExists(f string) bool { _, err := os.Stat(f); return err == nil }

// Send shows a notification. A later one with the same tag replaces it
// instead of stacking. sound names a sound from the freedesktop sound
// theme to play with it, "" for none.
func Send(tag, summary, body, sound string) error {
	mu.Lock()
	defer mu.Unlock()
	if conn == nil || !conn.Connected() {
		c, err := dbus.ConnectSessionBus()
		if err != nil {
			return err
		}
		conn = c
	}
	hints := map[string]dbus.Variant{"desktop-entry": dbus.MakeVariant("steelseries-gg")}
	if sound != "" {
		hints["sound-name"] = dbus.MakeVariant(sound)
	} else {
		hints["suppress-sound"] = dbus.MakeVariant(true)
	}
	var id uint32
	err := conn.Object(service, path).Call(service+".Notify", 0,
		"SteelSeries GG", ids[tag], icon(), summary, body, []string{}, hints, int32(-1)).Store(&id)
	if err == nil {
		ids[tag] = id
	}
	return err
}
