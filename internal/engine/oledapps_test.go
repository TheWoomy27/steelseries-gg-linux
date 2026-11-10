package engine

import (
	"os"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

func TestMemAndCPU(t *testing.T) {
	if p := memPercent(); p <= 0 || p > 100 {
		t.Errorf("memory %d%%", p)
	}
	var m cpuMeter
	m.percent()
	time.Sleep(50 * time.Millisecond)
	if p := m.percent(); p < 0 || p > 100 {
		t.Errorf("cpu %d%%", p)
	}
}

// TestNowPlayingLive plays a made-up song on a fake MPRIS player on the
// session bus for a few seconds. It runs with GG_LINUX_MPRIS_LIVE set;
// a running ggd shows the song on the keyboard's screen meanwhile.
func TestNowPlayingLive(t *testing.T) {
	if os.Getenv("GG_LINUX_MPRIS_LIVE") == "" {
		t.Skip("GG_LINUX_MPRIS_LIVE not set")
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	name := "org.mpris.MediaPlayer2.gglinuxtest"
	if reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("name: %v %v", reply, err)
	}
	props := prop.Map{"org.mpris.MediaPlayer2.Player": {
		"PlaybackStatus": {Value: "Playing"},
		"Metadata": {Value: map[string]dbus.Variant{
			"xesam:title":  dbus.MakeVariant("gg-linux test song"),
			"xesam:artist": dbus.MakeVariant([]string{"The Testers"}),
		}},
	}}
	if _, err := prop.Export(conn, "/org/mpris/MediaPlayer2", props); err != nil {
		t.Fatal(err)
	}
	np := &nowPlaying{}
	defer np.close()
	song := np.playing()
	if song["title"] != "gg-linux test song" || song["artist"] != "The Testers" {
		t.Fatalf("playing: %v", song)
	}
	time.Sleep(5 * time.Second)
}
