// Package events implements the GG websocket event channels (/sock,
// /eventing). Messages are JSON objects {"event": NAME, "data": ...}.
//
// Clients authenticate in-band: the server asks with
// EVENT_SOCKET_AUTHENTICATION_REQUESTED (renderer) or GG_API_AUTH_TOKEN
// (Electron main process) and the client answers with
// EVENT_SOCKET_AUTHENTICATION_TOKEN {token}.
package events

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Message struct {
	Event string `json:"event"`
	Data  any    `json:"data,omitempty"`
}

type client struct {
	conn   *websocket.Conn
	send   chan []byte
	authed bool
}

// Handler receives messages a client sends (other than authentication).
type Handler func(event string, data json.RawMessage)

type Hub struct {
	name     string
	token    string
	mu       sync.Mutex
	clients  map[*client]bool
	handlers map[string]Handler
	upgrader websocket.Upgrader
	onAuth   func(send func(event string, data any))
}

// OnAuthenticated runs when a client has authenticated, so the server can
// replay current state (running sub-apps...) to that client only.
func (h *Hub) OnAuthenticated(fn func(send func(event string, data any))) {
	h.mu.Lock()
	h.onAuth = fn
	h.mu.Unlock()
}

func (h *Hub) sendTo(c *client, event string, data any) {
	b, err := json.Marshal(Message{Event: event, Data: data})
	if err != nil {
		return
	}
	defer func() { recover() }() // client may have gone away
	select {
	case c.send <- b:
	default:
	}
}

func NewHub(name, token string) *Hub {
	return &Hub{
		name:     name,
		token:    token,
		clients:  map[*client]bool{},
		handlers: map[string]Handler{},
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
	}
}

// On registers a handler for an inbound event.
func (h *Hub) On(event string, fn Handler) {
	h.mu.Lock()
	h.handlers[event] = fn
	h.mu.Unlock()
}

// Broadcast sends an event to every authenticated client.
func (h *Hub) Broadcast(event string, data any) {
	b, err := json.Marshal(Message{Event: event, Data: data})
	if err != nil {
		slog.Error("event marshal", "event", event, "err", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if !c.authed && h.token != "" {
			continue
		}
		select {
		case c.send <- b:
		default:
			slog.Warn("dropping event for slow client", "hub", h.name, "event", event)
		}
	}
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{conn: conn, send: make(chan []byte, 256), authed: h.token == ""}
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
	slog.Debug("ws connect", "hub", h.name, "path", r.URL.Path)

	go h.writer(c)
	if c.authed {
		h.replay(c)
	} else {
		for _, ev := range []string{"EVENT_SOCKET_AUTHENTICATION_REQUESTED", "GG_API_AUTH_TOKEN"} {
			b, _ := json.Marshal(Message{Event: ev})
			c.send <- b
		}
	}
	h.reader(c)
}

func (h *Hub) replay(c *client) {
	h.mu.Lock()
	fn := h.onAuth
	h.mu.Unlock()
	if fn != nil {
		fn(func(event string, data any) { h.sendTo(c, event, data) })
	}
}

func (h *Hub) reader(c *client) {
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
		close(c.send)
		c.conn.Close()
	}()
	c.conn.SetReadLimit(4 << 20)
	for {
		_, b, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var m struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if m.Event == "EVENT_SOCKET_AUTHENTICATION_TOKEN" {
			var d struct {
				Token string `json:"token"`
			}
			json.Unmarshal(m.Data, &d)
			h.mu.Lock()
			was := c.authed
			c.authed = d.Token == h.token
			h.mu.Unlock()
			if c.authed && !was {
				h.replay(c)
			}
			continue
		}
		h.mu.Lock()
		fn := h.handlers[m.Event]
		h.mu.Unlock()
		if fn != nil {
			fn(m.Event, m.Data)
		}
	}
}

func (h *Hub) writer(c *client) {
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case b, ok := <-c.send:
			if !ok {
				return
			}
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
				return
			}
		case <-ping.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
