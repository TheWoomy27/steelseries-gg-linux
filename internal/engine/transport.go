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

func (t hidTransport) Chunk(tg devicert.Target, kind, size int, data []byte, timeout time.Duration) ([]byte, error) {
	d, err := t.find(tg)
	if err != nil {
		return nil, err
	}
	reportID := uint8(0)
	if len(data) > 0 {
		reportID = data[0]
	}
	switch kind {
	case devicert.HIDIO, devicert.HIDIONoFail:
		return d.Exchange(data, size, timeout)
	case devicert.HIDNow:
		return nil, d.Write(data, size)
	case devicert.HIDFeature:
		return nil, d.SetFeature(data, size)
	case devicert.HIDReadOnly:
		return d.ReadInput(timeout)
	case devicert.HIDFeatureReadOnly:
		return d.GetFeature(reportID, size)
	case devicert.HIDFeatureOutFeatIn:
		if err := d.SetFeature(data, size); err != nil {
			return nil, err
		}
		return d.GetFeature(reportID, size)
	case devicert.HIDFeatureOutInputIn:
		if err := d.SetFeature(data, size); err != nil {
			return nil, err
		}
		return d.ReadInput(timeout)
	case devicert.HIDOutputFeatureIn:
		if err := d.Write(data, size); err != nil {
			return nil, err
		}
		return d.GetFeature(reportID, size)
	}
	return nil, fmt.Errorf("HID chunk type %d is not supported", kind)
}
