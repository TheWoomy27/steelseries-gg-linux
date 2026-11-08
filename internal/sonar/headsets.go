package sonar

import (
	"strconv"
	"strings"
)

// Headset is what GG's Engine knows about a SteelSeries headset's audio
// device: whether ChatMix is set on the headset and, for wireless
// headsets, whether the headset is connected to its dongle.
type Headset struct {
	ChatMixDial bool
	Wireless    bool
	Connected   bool
}

// Headsets is Engine's view of the headsets, by USB audio device
// (vendor<<16 | product). On Windows Sonar asks Engine over GG's websocket.
type Headsets interface {
	Headset(usb uint32) (Headset, bool)
	ChatMixLevels(usb uint32) (game, chat int, ok bool)
}

// SetHeadsets connects Sonar to Engine.
func (s *Service) SetHeadsets(h Headsets) {
	s.mu.Lock()
	s.headsets = h
	s.mu.Unlock()
}

func (s *Service) headsetSource() Headsets {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headsets
}

// usbIDs maps the sinks and sources of USB devices to the device's USB id,
// which ALSA nodes leave to their device object.
func usbIDs(objs []pwObject) map[string]uint32 {
	id := func(p map[string]any) (uint32, bool) {
		vendor, err1 := strconv.ParseUint(strings.TrimPrefix(propString(p, "device.vendor.id"), "0x"), 16, 16)
		product, err2 := strconv.ParseUint(strings.TrimPrefix(propString(p, "device.product.id"), "0x"), 16, 16)
		return uint32(vendor)<<16 | uint32(product), err1 == nil && err2 == nil
	}
	devices := map[int]uint32{}
	for _, o := range objs {
		if usb, ok := id(o.Info.Props); ok && o.Type == "PipeWire:Interface:Device" {
			devices[o.ID] = usb
		}
	}
	out := map[string]uint32{}
	for _, o := range objs {
		if o.Type != "PipeWire:Interface:Node" {
			continue
		}
		dev, _ := o.Info.Props["device.id"].(float64)
		usb, ok := id(o.Info.Props)
		if !ok {
			usb, ok = devices[int(dev)]
		}
		if ok {
			out[propString(o.Info.Props, "node.name")] = usb
		}
	}
	return out
}

// headsetOf finds what Engine knows about the headset a device belongs to.
func (s *Service) headsetOf(device string) (Headset, uint32, bool) {
	h := s.headsetSource()
	if h == nil || device == "" {
		return Headset{}, 0, false
	}
	objs, err := pwDump()
	if err != nil {
		return Headset{}, 0, false
	}
	usb, ok := usbIDs(objs)[device]
	if !ok {
		return Headset{}, 0, false
	}
	info, ok := h.Headset(usb)
	return info, usb, ok
}
