package notify

import (
	"bufio"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

type server struct{ got chan []any }

func (s server) Notify(app string, replaces uint32, icon, summary, body string, actions []string, hints map[string]dbus.Variant, timeout int32) (uint32, *dbus.Error) {
	s.got <- []any{app, replaces, summary, body}
	return 7, nil
}

// TestSend delivers notifications to a fake notification service on a
// private session bus.
func TestSend(t *testing.T) {
	daemon := exec.Command("dbus-daemon", "--session", "--nofork", "--print-address=1")
	out, err := daemon.StdoutPipe()
	if err == nil {
		err = daemon.Start()
	}
	if err != nil {
		t.Skip("dbus-daemon:", err)
	}
	t.Cleanup(func() { daemon.Process.Kill(); daemon.Wait() })
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", strings.TrimSpace(addr))

	srv, err := dbus.Connect(strings.TrimSpace(addr))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	s := server{make(chan []any, 2)}
	srv.Export(s, path, service)
	if _, err := srv.RequestName(service, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}

	for _, want := range [][]any{
		{"SteelSeries GG", uint32(0), "Low battery", "Aerox 5 Wireless: 5% left"},
		{"SteelSeries GG", uint32(7), "Low battery", "Aerox 5 Wireless: 4% left"},
	} {
		if err := Send("battery/1", "Low battery", want[3].(string), ""); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-s.got:
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("notification %v, want %v", got, want)
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no notification arrived")
		}
	}
}
