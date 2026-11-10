package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"ggl/internal/gamesense"
)

// The Engine's own screen apps, as Windows GG has them for OLED devices:
// Now Playing (the media playing, from MPRIS players), System Stats and a
// clock. They are GameSense games, listed and switched in GG's Apps page;
// games' own screens go over them. Now Playing is on at first, the other
// two off, as each would cover the device's own picture all the time.

type oledApp struct {
	game, event, display string
	priority             int
	on                   bool // on at first
	handler              string
}

var oledApps = []oledApp{
	{"NOW_PLAYING", "NOW_PLAYING", "Now Playing", -1, true,
		`{"device-type":"screened","zone":"one","mode":"screen","datas":[{"lines":[{"has-text":true,"context-frame-key":"title","bold":true},{"has-text":true,"context-frame-key":"artist"}]}]}`},
	{"STATS", "STATS", "System Stats", -2, false,
		`{"device-type":"screened","zone":"one","mode":"screen","datas":[{"lines":[{"has-text":true,"context-frame-key":"cpu","prefix":"CPU ","suffix":"%","bold":true},{"has-text":true,"context-frame-key":"ram","prefix":"RAM ","suffix":"%","bold":true}]}]}`},
	{"CLOCK", "CLOCK", "Clock", -2, false,
		`{"device-type":"screened","zone":"one","mode":"screen","datas":[{"lines":[{"has-text":true,"context-frame-key":"time","bold":true},{"has-text":true,"context-frame-key":"date"}]}]}`},
}

func (e *Engine) registerOLEDApps() {
	gs := e.gameSense.state
	var defaulted bool
	e.state.Get("oledAppsDefaulted", &defaulted)
	for _, a := range oledApps {
		gs.Metadata(a.game, a.display, "gg-linux", 0)
		gs.SetPriority(a.game, a.priority)
		gs.Bind(a.game, a.event, gamesense.EventInfo{Max: 100, ValueOptional: true}, []json.RawMessage{json.RawMessage(a.handler)})
		if !defaulted && !a.on {
			e.gameSense.setEnabled(a.game, false)
		}
	}
	e.state.Set("oledAppsDefaulted", true)
}

// send shows an app's frame (and keeps it showing).
func (e *Engine) sendOLEDApp(game string, frame map[string]string) {
	b, _ := json.Marshal(map[string]any{"value": 1, "frame": frame})
	e.gameSense.state.Event(game, game, b)
}

// runOLEDApps feeds the apps while a device with a screen is connected.
func (e *Engine) runOLEDApps(ctx context.Context) {
	e.registerOLEDApps()
	gs := e.gameSense.state
	np := &nowPlaying{}
	defer np.close()
	var cpu cpuMeter
	var lastNP map[string]string
	var lastStats, lastClock time.Time
	lastMinute := ""
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if !e.oledPresent() {
			continue
		}
		if gs.Enabled("NOW_PLAYING") {
			if song := np.playing(); song != nil {
				if !equalFrames(song, lastNP) {
					e.sendOLEDApp("NOW_PLAYING", song)
				} else {
					gs.Heartbeat("NOW_PLAYING")
				}
				lastNP = song
			} else if lastNP != nil {
				gs.Stop("NOW_PLAYING")
				lastNP = nil
			}
		}
		if gs.Enabled("STATS") && time.Since(lastStats) >= 2*time.Second {
			lastStats = time.Now()
			e.sendOLEDApp("STATS", map[string]string{"cpu": strconv.Itoa(cpu.percent()), "ram": strconv.Itoa(memPercent())})
		}
		if gs.Enabled("CLOCK") {
			now := time.Now()
			if m := now.Format("15:04"); m != lastMinute || time.Since(lastClock) > 5*time.Second {
				lastMinute, lastClock = m, now
				e.sendOLEDApp("CLOCK", map[string]string{"time": m, "date": now.Format("Mon 2 Jan")})
			}
		}
	}
}

func equalFrames(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// nowPlaying reads MPRIS players on the session bus.
type nowPlaying struct {
	conn   *dbus.Conn
	failed time.Time
	last   string // the player shown, kept while it plays
}

func (n *nowPlaying) close() {
	if n.conn != nil {
		n.conn.Close()
	}
}

// playing is the title and artist of the media playing, or nil.
func (n *nowPlaying) playing() map[string]string {
	if n.conn == nil {
		if time.Since(n.failed) < 30*time.Second {
			return nil
		}
		c, err := dbus.ConnectSessionBus()
		if err != nil {
			slog.Warn("now playing: session bus", "err", err)
			n.failed = time.Now()
			return nil
		}
		n.conn = c
	}
	var names []string
	if err := n.conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names); err != nil {
		n.conn.Close()
		n.conn, n.failed = nil, time.Now()
		return nil
	}
	var players []string
	for _, name := range names {
		// playerctld stands in for the other players: it would show
		// each twice.
		if strings.HasPrefix(name, "org.mpris.MediaPlayer2.") && name != "org.mpris.MediaPlayer2.playerctld" {
			players = append(players, name)
		}
	}
	// The player shown before goes first, so two playing at once do not
	// take turns.
	for i, p := range players {
		if p == n.last {
			players[0], players[i] = players[i], players[0]
		}
	}
	for _, p := range players {
		obj := n.conn.Object(p, "/org/mpris/MediaPlayer2")
		st, err := obj.GetProperty("org.mpris.MediaPlayer2.Player.PlaybackStatus")
		if err != nil || st.Value() != "Playing" {
			continue
		}
		md, err := obj.GetProperty("org.mpris.MediaPlayer2.Player.Metadata")
		if err != nil {
			continue
		}
		meta, _ := md.Value().(map[string]dbus.Variant)
		title, _ := meta["xesam:title"].Value().(string)
		var artist string
		switch a := meta["xesam:artist"].Value().(type) {
		case []string:
			artist = strings.Join(a, ", ")
		case string:
			artist = a
		}
		if title == "" {
			continue
		}
		n.last = p
		return map[string]string{"title": title, "artist": artist}
	}
	n.last = ""
	return nil
}

// cpuMeter measures CPU use between calls, from /proc/stat.
type cpuMeter struct{ idle, total uint64 }

func (m *cpuMeter) percent() int {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer f.Close()
	line, _ := bufio.NewReader(f).ReadString('\n')
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0
	}
	var idle, total uint64
	for i, s := range fields[1:] {
		v, _ := strconv.ParseUint(s, 10, 64)
		total += v
		if i == 3 || i == 4 { // idle, iowait
			idle += v
		}
	}
	dIdle, dTotal := idle-m.idle, total-m.total
	first := m.total == 0
	m.idle, m.total = idle, total
	if first || dTotal == 0 {
		return 0
	}
	return int(100 * (dTotal - dIdle) / dTotal)
}

// memPercent is the share of memory in use, from /proc/meminfo.
func memPercent() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var total, avail uint64
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			avail = v
		}
	}
	if total == 0 {
		return 0
	}
	return int(100 * (total - avail) / total)
}
