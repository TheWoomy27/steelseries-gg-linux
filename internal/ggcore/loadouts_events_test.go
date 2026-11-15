package ggcore

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"ggl/internal/httpx"
)

// The client's QuickSet page follows the active profile through the GG
// socket: EVENT_FOREGROUND_APP_OR_LOADOUT_CHANGE reloads the active profile,
// EVENT_LOADOUTS_LOADOUT_MODIFIED_V1 the profiles and the active one. Every
// way a profile gets deployed must send both, or the page keeps showing the
// previous profile (or misses a profile made outside it) until reopened.
func TestDeployNotifiesClient(t *testing.T) {
	c := testCore(t)
	c.srv = httpx.NewServer("gg", "t")
	c.routes()
	ts := httptest.NewTLSServer(c.srv)
	defer ts.Close()

	events := clientSocket(t, ts.URL, "t")
	post := func(path string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(`{"name":"Made outside","binding":"3"}`))
		req.Header.Set("Authorization", "Bearer t")
		resp, err := ts.Client().Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("POST %s: %v %v", path, resp, err)
		}
		resp.Body.Close()
	}
	// A profile made outside the page, as a game's profile is made from its
	// Sonar preset.
	post("/loadouts/addCustom")
	expect(t, events, "EVENT_LOADOUTS_LOADOUT_MODIFIED_V1", "")
	st := c.loadLoadouts()
	id := st.Loadouts[len(st.Loadouts)-1].ID

	for _, deploy := range []struct {
		name string
		run  func(id string)
	}{
		{"script or hotkey", func(id string) { post("/loadouts/" + id + "/deploy") }},
		{"client click", func(id string) { post("/v1/loadouts/" + id + "/deploy") }},
		{"game auto-switch", func(id string) { c.DeployLoadout(id, false) }},
	} {
		for _, target := range []string{id, defaultLoadoutID} {
			deploy.run(target)
			expect(t, events, "EVENT_LOADOUTS_LOADOUT_MODIFIED_V1", deploy.name)
			if got := expect(t, events, "EVENT_FOREGROUND_APP_OR_LOADOUT_CHANGE", deploy.name); got["loadoutId"] != target || got["hasLoadoutChange"] != true {
				t.Errorf("%s: change event %v, want profile %s", deploy.name, got, target)
			}
		}
	}
}

// clientSocket connects to /sock as the client's pages do and returns the
// events it receives.
func clientSocket(t *testing.T, url, token string) <-chan json.RawMessage {
	t.Helper()
	d := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	conn, _, err := d.Dial("wss://"+strings.TrimPrefix(url, "https://")+"/sock", http.Header{"Origin": {"file://"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	out := make(chan json.RawMessage, 64)
	authed := make(chan struct{})
	go func() {
		for {
			_, b, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m struct {
				Event string `json:"event"`
			}
			json.Unmarshal(b, &m)
			switch m.Event {
			case "EVENT_SOCKET_AUTHENTICATION_REQUESTED":
				conn.WriteJSON(map[string]any{"event": "EVENT_SOCKET_AUTHENTICATION_TOKEN", "data": map[string]string{"token": token}})
				// The hub marks the client authenticated once it reads this.
				time.AfterFunc(100*time.Millisecond, func() { close(authed) })
			case "GG_API_AUTH_TOKEN":
			default:
				out <- b
			}
		}
	}()
	select {
	case <-authed:
	case <-time.After(2 * time.Second):
		t.Fatal("no authentication request on /sock")
	}
	return out
}

// expect waits for event, skipping others, and returns its data.
func expect(t *testing.T, events <-chan json.RawMessage, event, context string) map[string]any {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case b := <-events:
			var m struct {
				Event string         `json:"event"`
				Data  map[string]any `json:"data"`
			}
			json.Unmarshal(b, &m)
			if m.Event == event {
				return m.Data
			}
		case <-timeout:
			t.Fatalf("%s: no %s on the client socket", context, event)
			return nil
		}
	}
}
