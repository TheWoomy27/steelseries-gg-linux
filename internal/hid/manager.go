package hid

import (
	"log/slog"
	"sort"
	"sync"
)

// Manager keeps every SteelSeries hidraw node open and notices hotplug.
type Manager struct {
	mu       sync.Mutex
	devices  map[string]*Device
	onChange func(added, removed []Info)
}

func NewManager(onChange func(added, removed []Info)) *Manager {
	return &Manager{devices: map[string]*Device{}, onChange: onChange}
}

// Scan reconciles open nodes with /sys/class/hidraw.
func (m *Manager) Scan() {
	infos, err := Enumerate()
	if err != nil {
		slog.Warn("hidraw enumerate", "err", err)
		return
	}
	seen := map[string]bool{}
	var added, removed []Info
	m.mu.Lock()
	for _, info := range infos {
		seen[info.Path] = true
		if d, ok := m.devices[info.Path]; ok && d.ProductID == info.ProductID {
			continue
		}
		d, err := Open(info)
		if err != nil {
			slog.Debug("hidraw open failed", "path", info.Path, "pid", info.ProductID, "err", err)
			continue
		}
		m.devices[info.Path] = d
		added = append(added, info)
	}
	for path, d := range m.devices {
		if !seen[path] {
			d.Close()
			delete(m.devices, path)
			removed = append(removed, d.Info)
		}
	}
	m.mu.Unlock()
	if (len(added) > 0 || len(removed) > 0) && m.onChange != nil {
		m.onChange(added, removed)
	}
}

// Find returns the open node exposing (page, usage) for a product id.
// iface >= 0 additionally pins the USB interface number.
func (m *Manager) Find(pid, page, usage uint16, iface int) *Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	var cands []*Device
	for _, d := range m.devices {
		if d.ProductID != pid {
			continue
		}
		if iface >= 0 && d.Interface != iface {
			continue
		}
		if page == 0 && usage == 0 || d.Desc.Has(page, usage) {
			cands = append(cands, d)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Interface < cands[j].Interface })
	return cands[0]
}

// Products lists the product ids currently connected.
func (m *Manager) Products() map[uint16][]Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[uint16][]Info{}
	for _, d := range m.devices {
		out[d.ProductID] = append(out[d.ProductID], d.Info)
	}
	return out
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for p, d := range m.devices {
		d.Close()
		delete(m.devices, p)
	}
}
