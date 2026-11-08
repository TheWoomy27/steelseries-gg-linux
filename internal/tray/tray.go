// Package tray puts an icon in the desktop's notification area through the
// StatusNotifierItem D-Bus protocol, which KDE Plasma, waybar, most other
// Wayland bars and GNOME's AppIndicator extension host, with a
// com.canonical.dbusmenu context menu.
package tray

import (
	"fmt"
	"image"
	"image/color"
	"log/slog"
	"os"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

const (
	itemPath    = dbus.ObjectPath("/StatusNotifierItem")
	itemIface   = "org.kde.StatusNotifierItem"
	watcherName = "org.kde.StatusNotifierWatcher"
)

// Options describe the tray icon.
type Options struct {
	ID         string        // application id, e.g. "steelseries-gg"
	Title      string        // also the tooltip
	Icon       []image.Image // the icon at one or more sizes
	IconName   string        // themed icon to use when Icon is empty
	OnActivate func()        // primary click on the icon
	OnClick    func(id string)
	OnMenuOpen func() // called before the menu opens, so it can be refreshed
}

type Tray struct {
	conn  *dbus.Conn
	name  string
	opts  Options
	props *prop.Properties
	menu  *menu
}

// Start connects to the session bus and registers the icon with the
// notification area. It keeps re-registering whenever the host restarts.
func Start(opts Options) (*Tray, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	t := &Tray{conn: conn, opts: opts, name: fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())}
	if err := t.export(); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := conn.RequestName(t.name, dbus.NameFlagDoNotQueue); err != nil {
		conn.Close()
		return nil, err
	}

	conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, watcherName),
	)
	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)
	go func() {
		for s := range signals {
			if s.Name == "org.freedesktop.DBus.NameOwnerChanged" && len(s.Body) == 3 && s.Body[2] != "" {
				t.register()
			}
		}
	}()
	t.register()
	return t, nil
}

func (t *Tray) export() error {
	sni := (*item)(t)
	if err := t.conn.Export(sni, itemPath, itemIface); err != nil {
		return err
	}
	props, err := prop.Export(t.conn, itemPath, prop.Map{itemIface: {
		"Category":            {Value: "ApplicationStatus"},
		"Id":                  {Value: t.opts.ID},
		"Title":               {Value: t.opts.Title},
		"Status":              {Value: "Active"},
		"WindowId":            {Value: int32(0)},
		"IconName":            {Value: t.opts.IconName},
		"IconPixmap":          {Value: pixmaps(t.opts.Icon)},
		"OverlayIconName":     {Value: ""},
		"OverlayIconPixmap":   {Value: []pixmap{}},
		"AttentionIconName":   {Value: ""},
		"AttentionIconPixmap": {Value: []pixmap{}},
		"AttentionMovieName":  {Value: ""},
		"ToolTip":             {Value: tooltip{Icon: []pixmap{}, Title: t.opts.Title}},
		"ItemIsMenu":          {Value: false},
		"Menu":                {Value: menuPath},
		"IconThemePath":       {Value: ""},
	}})
	if err != nil {
		return err
	}
	t.props = props
	node := &introspect.Node{Interfaces: []introspect.Interface{
		introspect.IntrospectData,
		prop.IntrospectData,
		{Name: itemIface, Methods: introspect.Methods(sni), Properties: props.Introspection(itemIface)},
	}}
	if err := t.conn.Export(introspect.NewIntrospectable(node), itemPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}
	t.menu, err = exportMenu(t.conn, t.opts.OnClick, t.opts.OnMenuOpen)
	return err
}

// register announces the icon to the notification area host, if one is
// running.
func (t *Tray) register() {
	err := t.conn.Object(watcherName, "/StatusNotifierWatcher").
		Call(watcherName+".RegisterStatusNotifierItem", 0, t.name).Err
	if err != nil {
		slog.Info("tray: no notification area to register with yet", "err", err)
	}
}

// SetMenu replaces the context menu.
func (t *Tray) SetMenu(items []Item) { t.menu.set(items) }

// SetIcon changes the icon.
func (t *Tray) SetIcon(icon []image.Image) {
	t.props.SetMust(itemIface, "IconPixmap", pixmaps(icon))
	t.conn.Emit(itemPath, itemIface+".NewIcon")
}

// SetToolTip changes the text shown when hovering the icon.
func (t *Tray) SetToolTip(text string) {
	t.props.SetMust(itemIface, "ToolTip", tooltip{Icon: []pixmap{}, Title: text})
	t.conn.Emit(itemPath, itemIface+".NewToolTip")
}

func (t *Tray) Close() { t.conn.Close() }

// item carries the org.kde.StatusNotifierItem methods.
type item Tray

func (t *item) Activate(x, y int32) *dbus.Error {
	if t.opts.OnActivate != nil {
		go t.opts.OnActivate()
	}
	return nil
}

func (t *item) SecondaryActivate(x, y int32) *dbus.Error { return nil }

// ContextMenu is only called by hosts that cannot show the dbusmenu
// themselves.
func (t *item) ContextMenu(x, y int32) *dbus.Error { return nil }

func (t *item) Scroll(delta int32, orientation string) *dbus.Error { return nil }

// pixmap is the protocol's (iiay) icon: width, height and ARGB32 pixels in
// network byte order.
type pixmap struct {
	Width, Height int32
	Data          []byte
}

type tooltip struct {
	IconName string
	Icon     []pixmap
	Title    string
	Text     string
}

func pixmaps(imgs []image.Image) []pixmap {
	out := []pixmap{}
	for _, img := range imgs {
		b := img.Bounds()
		p := pixmap{Width: int32(b.Dx()), Height: int32(b.Dy()), Data: make([]byte, 0, 4*b.Dx()*b.Dy())}
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
				p.Data = append(p.Data, c.A, c.R, c.G, c.B)
			}
		}
		out = append(out, p)
	}
	return out
}
