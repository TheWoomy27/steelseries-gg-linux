package engine

import (
	"fmt"
	"time"

	"ggl/internal/devicert"
	"ggl/internal/hid"
)

// hidTransport maps device-spec HID chunk types onto hidraw operations,
// matching what the Windows DeviceLib does with HidD_* / ReadFile /
// WriteFile.
type hidTransport struct {
	m *hid.Manager
}

func NewTransport(m *hid.Manager) devicert.Transport { return hidTransport{m} }

func (t hidTransport) find(tg devicert.Target) (*hid.Device, error) {
	d := t.m.Find(tg.ProductID, tg.UsagePage, tg.Usage, tg.Interface)
	if d == nil {
		return nil, fmt.Errorf("device %04x (usage %04x:%04x) is not connected", tg.ProductID, tg.UsagePage, tg.Usage)
	}
	return d, nil
}

func (t hidTransport) Connected(tg devicert.Target) bool {
	return t.m.Find(tg.ProductID, tg.UsagePage, tg.Usage, tg.Interface) != nil
}

func (t hidTransport) Chunk(tg devicert.Target, kind, size int, data []byte, read bool, gap, timeout time.Duration) ([]byte, error) {
	d, err := t.find(tg)
	if err != nil {
		return nil, err
	}
	if !read {
		return nil, send(d, kind, size, data)
	}
	if kind == devicert.HIDIO || kind == devicert.HIDIONoFail {
		return d.Exchange(data, size, timeout)
	}
	// Like DeviceLib's read: send the chunk's outgoing report, then fetch
	// the reply the way its chunk type says. DeviceLib waits the time
	// between commands before the fetch, which devices such as the legacy
	// Apex keyboards need to prepare a reply; a report the device pushes
	// is awaited at once instead, as nothing would catch it during a wait.
	if err := send(d, kind, size, data); err != nil {
		return nil, err
	}
	reportID := uint8(0)
	if len(data) > 0 {
		reportID = data[0]
	}
	switch kind {
	case devicert.HIDReadOnly, devicert.HIDFeatureOutInputIn:
		return d.ReadInput(timeout)
	case devicert.HIDNow:
		time.Sleep(gap)
		return d.GetInputReport(reportID, size)
	case devicert.HIDFeature, devicert.HIDFeatureReadOnly, devicert.HIDFeatureOutFeatIn, devicert.HIDOutputFeatureIn:
		if kind != devicert.HIDFeatureReadOnly {
			time.Sleep(gap)
		}
		return d.GetFeature(reportID, size)
	}
	return nil, fmt.Errorf("HID chunk type %d is not supported", kind)
}

// send performs only the outgoing half of a chunk, as DeviceLib does for
// chunks that don't read: many devices never answer a write, and a
// read-only chunk then does nothing.
func send(d *hid.Device, kind, size int, data []byte) error {
	switch kind {
	case devicert.HIDIO, devicert.HIDIONoFail, devicert.HIDNow, devicert.HIDOutputFeatureIn:
		return d.Write(data, size)
	case devicert.HIDFeature, devicert.HIDFeatureOutFeatIn, devicert.HIDFeatureOutInputIn:
		return d.SetFeature(data, size)
	case devicert.HIDReadOnly, devicert.HIDFeatureReadOnly:
		return nil
	}
	return fmt.Errorf("HID chunk type %d is not supported", kind)
}
