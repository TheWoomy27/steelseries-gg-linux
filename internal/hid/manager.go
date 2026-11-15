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

// Scan reconciles open nodes with /sys/class/hidraw. Removals are
// reported before additions, so a device plugged back in between two
// scans is seen to leave and come back.
func (m *Manager) Scan() {
	infos, err := Enumerate()
	if err != nil {
		slog.Warn("hidraw enumerate", "err", err)
		return
	}
	present := map[string]Info{}
	for _, info := range infos {
		present[info.Path] = info
	}
	var removed []Info
	m.mu.Lock()
	for path, d := range m.devices {
		// A node can be freed by an unplug and given to the next device
		// plugged in, the same one again after a USB reset or a resume
		// from suspend: the node open here is dead then.
		if info, ok := present[path]; ok && info.ProductID == d.ProductID && info.instance == d.instance {
			continue
		}
		d.Close()
		delete(m.devices, path)
		removed = append(removed, d.Info)
	}
	m.mu.Unlock()
	if len(removed) > 0 && m.onChange != nil {
		m.onChange(nil, removed)
	}
	var added []Info
	m.mu.Lock()
	for _, info := range infos {
		if _, ok := m.devices[info.Path]; ok {
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
	m.mu.Unlock()
	if len(added) > 0 && m.onChange != nil {
		m.onChange(added, nil)
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

// Devices lists the open nodes of a product.
func (m *Manager) Devices(pid uint16) []*Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Device
	for _, d := range m.devices {
		if d.ProductID == pid {
			out = append(out, d)
		}
	}
	return out
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
