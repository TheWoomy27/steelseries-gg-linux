package hotkeys

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os/exec"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

// The desktop portal's GlobalShortcuts interface: ggd opens a session,
// binds its shortcuts in it (each with an id, a description and the key
// GG has for it as the preferred trigger) and hears Activated when one is
// pressed. The desktop owns the keys: KDE asks the user to confirm new
// shortcuts once, keeps them under the app's name in System Settings →
// Shortcuts, and from then on uses the key set there, whatever GG's
// preferred trigger becomes.
//
// The portal tells apps apart by app id. ggd runs as a systemd service
// (inside a distrobox, too), not as an app the desktop launched, so it has
// none of its own; it registers the id of GG's desktop entry first
// (org.freedesktop.host.portal.Registry, portal 1.19 and later), on a
// connection of its own as registering must come before any other portal
// call. Without it KDE names the shortcuts after the session token, which
// ggd keeps fixed so they are still found again.

const (
	portalService  = "org.freedesktop.portal.Desktop"
	portalPath     = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	shortcutsIface = "org.freedesktop.portal.GlobalShortcuts"
	requestIface   = "org.freedesktop.portal.Request"
	sessionIface   = "org.freedesktop.portal.Session"
	registryIface  = "org.freedesktop.host.portal.Registry"

	// AppID is GG's desktop entry (packaging/steelseries-gg.desktop).
	AppID = "steelseries-gg"
	// sessionToken names ggd's session; KDE names unregistered apps'
	// shortcuts after it.
	sessionToken = "steelseries_gg"
)

// Portal keeps a set of global shortcuts through the desktop portal.
type Portal struct {
	conn *dbus.Conn
	run  func(command string) // runs an activated shortcut's command

	mu      sync.Mutex
	want    map[string]Bind // key spec → bind
	byID    map[string]Bind // the session's binds, by shortcut id
	session dbus.ObjectPath
	waiting map[dbus.ObjectPath]chan *dbus.Signal // requests, by handle
	kick    chan struct{}
	token   int
}

// NewPortal connects to the session bus's desktop portal; it fails when
// the portal has no GlobalShortcuts interface.
func NewPortal(ctx context.Context) (*Portal, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	p, err := newPortal(ctx, conn, runCommand)
	if err != nil {
		conn.Close()
		return nil, err
	}
	go func() { <-ctx.Done(); p.closeSession(); conn.Close() }()
	return p, nil
}

func newPortal(ctx context.Context, conn *dbus.Conn, run func(string)) (*Portal, error) {
	p := &Portal{conn: conn, run: run, want: map[string]Bind{}, waiting: map[dbus.ObjectPath]chan *dbus.Signal{},
		kick: make(chan struct{}, 1)}
	p.register()
	v, err := conn.Object(portalService, portalPath).GetProperty(shortcutsIface + ".version")
	if err != nil {
		return nil, err
	}
	if ver, _ := v.Value().(uint32); ver < 1 {
		return nil, fmt.Errorf("GlobalShortcuts version %v", v.Value())
	}
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	for _, m := range [][]dbus.MatchOption{
		{dbus.WithMatchInterface(shortcutsIface), dbus.WithMatchMember("Activated")},
		{dbus.WithMatchInterface(requestIface), dbus.WithMatchMember("Response")},
		{dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, portalService)},
	} {
		if err := conn.AddMatchSignal(m...); err != nil {
			return nil, err
		}
	}
	go p.signals(signals)
	go p.sync(ctx)
	slog.Info("hotkeys: binding through the desktop portal", "version", v.Value())
	return p, nil
}

// register gives the connection GG's app id, where the portal can.
func (p *Portal) register() {
	err := p.conn.Object(portalService, portalPath).Call(registryIface+".Register", 0, AppID, map[string]dbus.Variant{}).Err
	if err != nil {
		slog.Info("hotkeys: portal app id not registered; shortcuts are listed under the session's name", "err", err)
	}
}

// Set makes exactly binds bound. Binding happens in the background, as the
// desktop may ask the user first.
func (p *Portal) Set(binds map[string]Bind) {
	p.mu.Lock()
	p.want = maps.Clone(binds)
	p.mu.Unlock()
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// byIDOf indexes binds by shortcut id; a bind without one is left out.
func byIDOf(binds map[string]Bind) map[string]Bind {
	out := map[string]Bind{}
	for _, b := range binds {
		if b.ID != "" {
			out[b.ID] = b
		}
	}
	return out
}

// sync brings the portal session in line with the wanted binds. Portals
// take a session's shortcuts once (GNOME's), so a change binds them in a
// new session; shortcuts the desktop knows come back without asking.
func (p *Portal) sync(ctx context.Context) {
	var bound map[string]Bind // spec → bind, as bound; nil: nothing yet
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.kick:
		}
		p.mu.Lock()
		want := p.want
		reset := p.session == "" && bound != nil // the portal restarted
		p.mu.Unlock()
		if reset {
			bound = nil
		}
		if bound != nil && maps.Equal(want, bound) {
			continue
		}
		p.closeSession()
		bound = map[string]Bind{}
		if len(want) == 0 {
			continue
		}
		if err := p.bind(ctx, want); err != nil {
			slog.Warn("hotkeys: portal", "err", err)
			p.closeSession()
			continue
		}
		bound = want
	}
}

func (p *Portal) bind(ctx context.Context, binds map[string]Bind) error {
	res, err := p.request(ctx, "CreateSession", map[string]dbus.Variant{"session_handle_token": dbus.MakeVariant(sessionToken)})
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	var session dbus.ObjectPath
	switch h := res["session_handle"].Value().(type) {
	case string:
		session = dbus.ObjectPath(h)
	case dbus.ObjectPath:
		session = h
	}
	if !session.IsValid() {
		return fmt.Errorf("create session: no session handle")
	}
	p.mu.Lock()
	p.session = session
	p.byID = byIDOf(binds)
	p.mu.Unlock()

	type shortcut struct {
		ID      string
		Options map[string]dbus.Variant
	}
	var list []shortcut
	for spec, b := range binds {
		if b.ID == "" {
			continue
		}
		list = append(list, shortcut{b.ID, map[string]dbus.Variant{
			"description":       dbus.MakeVariant(b.Description),
			"preferred_trigger": dbus.MakeVariant(portalTrigger(spec)),
		}})
	}
	res, err = p.request(ctx, "BindShortcuts", session, list, "")
	if err != nil {
		return fmt.Errorf("bind shortcuts: %w", err)
	}
	var got []struct {
		ID      string
		Options map[string]dbus.Variant
	}
	if v, ok := res["shortcuts"]; ok {
		dbus.Store([]any{v.Value()}, &got)
	}
	for _, s := range got {
		trigger, _ := s.Options["trigger_description"].Value().(string)
		slog.Info("hotkeys: bound", "shortcut", s.ID, "key", trigger)
	}
	return nil
}

// request calls a GlobalShortcuts method that answers through a Request
// object, and waits for its results. args go before the options, which
// carry the handle token.
func (p *Portal) request(ctx context.Context, method string, args ...any) (map[string]dbus.Variant, error) {
	p.mu.Lock()
	p.token++
	token := fmt.Sprintf("gg%d", p.token)
	p.mu.Unlock()
	// The portal answers on a path made from the caller's name and the
	// token; it is listened on before the call, so no answer is missed.
	sender := strings.ReplaceAll(strings.TrimPrefix(p.conn.Names()[0], ":"), ".", "_")
	handle := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)
	ch := make(chan *dbus.Signal, 1)
	p.mu.Lock()
	p.waiting[handle] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiting, handle)
		p.mu.Unlock()
	}()

	opts := map[string]dbus.Variant{"handle_token": dbus.MakeVariant(token)}
	if n := len(args); n > 0 {
		if o, ok := args[n-1].(map[string]dbus.Variant); ok {
			maps.Copy(opts, o)
			args = args[:n-1]
		}
	}
	var got dbus.ObjectPath
	if err := p.conn.Object(portalService, portalPath).CallWithContext(ctx, shortcutsIface+"."+method, 0, append(args, opts)...).Store(&got); err != nil {
		return nil, err
	}
	if got != handle { // portals before 0.9 picked their own path
		p.mu.Lock()
		delete(p.waiting, handle)
		p.waiting[got] = ch
		p.mu.Unlock()
		defer func() {
			p.mu.Lock()
			delete(p.waiting, got)
			p.mu.Unlock()
		}()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case sig := <-ch:
		var code uint32
		var res map[string]dbus.Variant
		if err := dbus.Store(sig.Body, &code, &res); err != nil {
			return nil, err
		}
		switch code {
		case 0:
			return res, nil
		case 1:
			return nil, fmt.Errorf("the user cancelled")
		default:
			return nil, fmt.Errorf("the portal failed (response %d)", code)
		}
	}
}

// signals hands the portal's signals out: request answers to their
// waiters, shortcut presses to their commands.
func (p *Portal) signals(ch chan *dbus.Signal) {
	for sig := range ch {
		switch sig.Name {
		case requestIface + ".Response":
			p.mu.Lock()
			w := p.waiting[sig.Path]
			p.mu.Unlock()
			if w != nil {
				select {
				case w <- sig:
				default:
				}
			}
		case shortcutsIface + ".Activated":
			var session dbus.ObjectPath
			var id string
			if len(sig.Body) < 2 || dbus.Store(sig.Body[:2], &session, &id) != nil {
				continue
			}
			p.mu.Lock()
			b, ok := p.byID[id]
			ok = ok && session == p.session
			p.mu.Unlock()
			if ok {
				slog.Debug("hotkeys: activated", "shortcut", id)
				go p.run(b.Command)
			}
		case "org.freedesktop.DBus.NameOwnerChanged":
			// A restarted portal has forgotten the session and the
			// connection's app id: register again and bind anew.
			var name, old, owner string
			if dbus.Store(sig.Body, &name, &old, &owner) != nil || name != portalService || owner == "" {
				continue
			}
			p.mu.Lock()
			p.session, p.byID = "", nil
			p.mu.Unlock()
			p.register()
			select {
			case p.kick <- struct{}{}:
			default:
			}
		}
	}
}

func (p *Portal) closeSession() {
	p.mu.Lock()
	s := p.session
	p.session, p.byID = "", nil
	p.mu.Unlock()
	if s != "" {
		p.conn.Object(portalService, s).Call(sessionIface+".Close", 0)
	}
}

// portalTrigger writes a key spec ("SHIFT ALT,M") as the XDG shortcuts
// specification does ("SHIFT+ALT+m"): letters are lower case, Super is
// LOGO, other keys keep their XKB names.
func portalTrigger(spec string) string {
	mods, key, _ := strings.Cut(spec, ",")
	var parts []string
	for _, m := range strings.Fields(mods) {
		if m == "SUPER" {
			m = "LOGO"
		}
		parts = append(parts, m)
	}
	if len(key) == 1 {
		key = strings.ToLower(key)
	}
	return strings.Join(append(parts, key), "+")
}

// runCommand runs a shortcut's command the way Hyprland's exec does.
func runCommand(command string) {
	if out, err := exec.Command("sh", "-c", command).CombinedOutput(); err != nil {
		slog.Warn("hotkeys: shortcut command", "command", command, "err", err, "output", strings.TrimSpace(string(out)))
	}
}
