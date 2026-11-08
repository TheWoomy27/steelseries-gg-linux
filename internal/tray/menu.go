package tray

import (
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

const (
	menuPath  = dbus.ObjectPath("/MenuBar")
	menuIface = "com.canonical.dbusmenu"
)

// Item is one menu entry.
type Item struct {
	ID        string // passed to Options.OnClick; clicking an item without one does nothing
	Label     string
	Checked   bool   // shows a checkmark
	Disabled  bool   // grayed out
	IconName  string // themed icon shown next to the label
	Separator bool
	Children  []Item // makes the item a submenu
}

// menu serves the com.canonical.dbusmenu interface. Entries get numeric ids
// in tree order each time the menu is replaced; 0 is the root.
type menu struct {
	conn    *dbus.Conn
	onClick func(id string)
	onOpen  func()

	mu       sync.Mutex
	revision uint32
	nodes    map[int32]*node
}

type node struct {
	id       int32
	item     Item
	children []int32
}

// layout is the dbusmenu (ia{sv}av) layout: id, properties, and children
// as variants of the same structure.
type layout struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

type itemProps struct {
	ID    int32
	Props map[string]dbus.Variant
}

type menuEvent struct {
	ID        int32
	EventID   string
	Data      dbus.Variant
	Timestamp uint32
}

func exportMenu(conn *dbus.Conn, onClick func(string), onOpen func()) (*menu, error) {
	m := &menu{conn: conn, onClick: onClick, onOpen: onOpen, nodes: map[int32]*node{0: {}}}
	if err := conn.Export(m, menuPath, menuIface); err != nil {
		return nil, err
	}
	props, err := prop.Export(conn, menuPath, prop.Map{menuIface: {
		"Version":       {Value: uint32(3)},
		"TextDirection": {Value: "ltr"},
		"Status":        {Value: "normal"},
		"IconThemePath": {Value: []string{}},
	}})
	if err != nil {
		return nil, err
	}
	node := &introspect.Node{Interfaces: []introspect.Interface{
		introspect.IntrospectData,
		prop.IntrospectData,
		{Name: menuIface, Methods: introspect.Methods(m), Properties: props.Introspection(menuIface)},
	}}
	return m, conn.Export(introspect.NewIntrospectable(node), menuPath, "org.freedesktop.DBus.Introspectable")
}

func (m *menu) set(items []Item) {
	m.mu.Lock()
	m.nodes = map[int32]*node{0: {}}
	next := int32(1)
	var add func(parent *node, items []Item)
	add = func(parent *node, items []Item) {
		for _, it := range items {
			n := &node{id: next, item: it}
			next++
			m.nodes[n.id] = n
			parent.children = append(parent.children, n.id)
			add(n, it.Children)
		}
	}
	add(m.nodes[0], items)
	m.revision++
	rev := m.revision
	m.mu.Unlock()
	m.conn.Emit(menuPath, menuIface+".LayoutUpdated", rev, int32(0))
}

func (n *node) props(names []string) map[string]dbus.Variant {
	p := map[string]dbus.Variant{}
	it := n.item
	switch {
	case n.id == 0:
	case it.Separator:
		p["type"] = dbus.MakeVariant("separator")
	default:
		// dbusmenu reads "_" as a mnemonic marker.
		p["label"] = dbus.MakeVariant(strings.ReplaceAll(it.Label, "_", "__"))
		if it.Disabled {
			p["enabled"] = dbus.MakeVariant(false)
		}
		if it.IconName != "" {
			p["icon-name"] = dbus.MakeVariant(it.IconName)
		}
		if it.Checked {
			p["toggle-type"] = dbus.MakeVariant("checkmark")
			p["toggle-state"] = dbus.MakeVariant(int32(1))
		}
	}
	if len(n.children) > 0 {
		p["children-display"] = dbus.MakeVariant("submenu")
	}
	if len(names) > 0 {
		for k := range p {
			if !contains(names, k) {
				delete(p, k)
			}
		}
	}
	return p
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (m *menu) layout(n *node, depth int32, names []string) layout {
	l := layout{ID: n.id, Props: n.props(names), Children: []dbus.Variant{}}
	if depth != 0 {
		for _, id := range n.children {
			l.Children = append(l.Children, dbus.MakeVariant(m.layout(m.nodes[id], depth-1, names)))
		}
	}
	return l
}

// com.canonical.dbusmenu methods.

func (m *menu) GetLayout(parentID, depth int32, names []string) (uint32, layout, *dbus.Error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.nodes[parentID]
	if n == nil {
		return 0, layout{}, dbus.MakeFailedError(errNoItem)
	}
	return m.revision, m.layout(n, depth, names), nil
}

func (m *menu) GetGroupProperties(ids []int32, names []string) ([]itemProps, *dbus.Error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []itemProps{}
	if len(ids) == 0 {
		for id := range m.nodes {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if n := m.nodes[id]; n != nil {
			out = append(out, itemProps{id, n.props(names)})
		}
	}
	return out, nil
}

func (m *menu) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n := m.nodes[id]; n != nil {
		if v, ok := n.props(nil)[name]; ok {
			return v, nil
		}
	}
	return dbus.Variant{}, dbus.MakeFailedError(errNoItem)
}

func (m *menu) Event(id int32, eventID string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventID != "clicked" {
		return nil
	}
	m.mu.Lock()
	n := m.nodes[id]
	m.mu.Unlock()
	if n == nil {
		return dbus.MakeFailedError(errNoItem)
	}
	if n.item.ID != "" && !n.item.Disabled && m.onClick != nil {
		go m.onClick(n.item.ID)
	}
	return nil
}

func (m *menu) EventGroup(events []menuEvent) ([]int32, *dbus.Error) {
	missing := []int32{}
	for _, e := range events {
		if err := m.Event(e.ID, e.EventID, e.Data, e.Timestamp); err != nil {
			missing = append(missing, e.ID)
		}
	}
	return missing, nil
}

// AboutToShow lets the owner refresh the menu before the host shows it; the
// host re-reads the layout when this reports a change.
func (m *menu) AboutToShow(id int32) (bool, *dbus.Error) {
	if id != 0 || m.onOpen == nil {
		return false, nil
	}
	m.mu.Lock()
	before := m.revision
	m.mu.Unlock()
	m.onOpen()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.revision != before, nil
}

func (m *menu) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	return []int32{}, []int32{}, nil
}

type menuError string

func (e menuError) Error() string { return string(e) }

const errNoItem = menuError("no such menu item")
