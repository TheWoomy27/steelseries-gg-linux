// Package httpx holds the HTTP plumbing shared by the GG core, Engine and
// Sonar servers: CORS for the file:// client, bearer auth, JSON helpers and
// request logging.
package httpx

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strings"
	"time"
)

// Server wraps a ServeMux with the middleware every GG service uses.
type Server struct {
	Name  string
	Mux   *http.ServeMux
	Token string // if set, requests must carry "Authorization: Bearer <Token>"
	Open  map[string]bool
}

func NewServer(name, token string) *Server {
	return &Server{Name: name, Mux: http.NewServeMux(), Token: token, Open: map[string]bool{}}
}

// Handle registers a Go 1.22 pattern such as "GET /device/{id}".
func (s *Server) Handle(pattern string, h http.HandlerFunc) { s.Mux.HandleFunc(pattern, h) }

// Public marks a path as reachable without the bearer token.
func (s *Server) Public(path string) { s.Open[path] = true }

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Hijack lets websocket upgrades through the logging wrapper.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("hijack not supported")
	}
	w.status = http.StatusSwitchingProtocols
	return h.Hijack()
}

// clientOrigin reports whether a browser request comes from the GG client
// itself, whose pages load from file://.
func clientOrigin(origin string) bool { return origin == "file://" || origin == "null" }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	// Only the client's own pages may call these local APIs from a
	// browser context; any web page origin is refused outright.
	if origin != "" && !clientOrigin(origin) {
		Error(w, http.StatusForbidden, "origin not allowed")
		return
	}
	h := w.Header()
	if origin != "" {
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Requests from outside a browser (no Origin and no Sec-Fetch-*
	// headers: the Electron main process, scripts) must carry the bearer
	// token. Browser requests without an Origin are same-site navigations
	// whose responses a web page cannot read.
	fromBrowser := origin != "" || r.Header.Get("Sec-Fetch-Mode") != ""
	if s.Token != "" && !fromBrowser && !s.Open[r.URL.Path] && !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		if r.Header.Get("Authorization") != "Bearer "+s.Token {
			slog.Warn("unauthorized", "svc", s.Name, "method", r.Method, "path", r.URL.Path)
			Error(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	}
	// The client sometimes builds URLs with a doubled slash.
	if strings.Contains(r.URL.Path, "//") {
		r.URL.Path = path.Clean(r.URL.Path)
	}
	start := time.Now()
	sw := &statusWriter{ResponseWriter: w, status: 200}
	_, pattern := s.Mux.Handler(r)
	if pattern == "" {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		slog.Warn("UNHANDLED", "svc", s.Name, "method", r.Method, "path", r.URL.RequestURI(), "body", string(body))
		Error(w, http.StatusNotFound, "not implemented on linux")
		return
	}
	s.Mux.ServeHTTP(sw, r)
	slog.Debug("req", "svc", s.Name, "method", r.Method, "path", r.URL.RequestURI(), "status", sw.status, "ms", time.Since(start).Milliseconds())
}

// JSON writes v with status 200.
func JSON(w http.ResponseWriter, v any) { JSONStatus(w, http.StatusOK, v) }

func JSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		w.Write([]byte("{}"))
		return
	}
	json.NewEncoder(w).Encode(v)
}

func Error(w http.ResponseWriter, status int, msg string) {
	JSONStatus(w, status, map[string]string{"error": msg})
}

// Decode reads a JSON body into v. An empty body leaves v untouched.
func Decode(r *http.Request, v any) error {
	b, err := io.ReadAll(r.Body)
	if err != nil || len(b) == 0 {
		return err
	}
	return json.Unmarshal(b, v)
}

// NoContent responds 200 with an empty JSON object, which is what most GG
// "fire and forget" endpoints return.
func OK(w http.ResponseWriter) { JSON(w, map[string]any{}) }

func RandomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
