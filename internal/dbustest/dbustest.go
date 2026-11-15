// Package dbustest starts private D-Bus session buses for tests.
package dbustest

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// config is a session bus that starts no services: the default session
// config lets a bus start any installed one (the desktop portal, a
// notification daemon) when a test calls its name.
const config = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:tmpdir=/tmp</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

// Bus starts a private session bus for the test and returns its address.
// It skips the test when dbus-daemon isn't installed.
func Bus(t *testing.T) string {
	t.Helper()
	conf := filepath.Join(t.TempDir(), "bus.conf")
	if err := os.WriteFile(conf, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	daemon := exec.Command("dbus-daemon", "--config-file="+conf, "--nofork", "--print-address=1")
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
	return strings.TrimSpace(addr)
}
