package prism

import (
	"math"
	"strconv"

	"ggl/internal/devicert"
	"ggl/internal/engine"
)

// zone is device_service_types.CachePrismSyncZone, with where its colour
// goes in the device's frame.
type zone struct {
	UniqueID                   string `json:"uniqueId"`
	X                          uint16 `json:"x"`
	Y                          uint16 `json:"y"`
	BitmapX                    uint8  `json:"bitmapX"`
	BitmapY                    uint8  `json:"bitmapY"`
	HidCode                    uint8  `json:"hidCode"`
	ActuationLevel             uint16 `json:"actuationLevel"`
	LinearIndex                uint8  `json:"linearIndex"`
	DeviceID                   int    `json:"deviceId"`
	DeviceHasBitmapCoordinates bool   `json:"deviceHasBitmapCoordinates"`
	DeviceHasLinearIndexing    bool   `json:"deviceHasLinearIndexing"`
	DeviceType                 int    `json:"deviceType"`
	DeviceBitmapXMin           uint8  `json:"deviceBitmapXMin"`
	DeviceBitmapXMax           uint8  `json:"deviceBitmapXMax"`
	DeviceBitmapXSize          uint8  `json:"deviceBitmapXSize"`
	DeviceBitmapYMin           uint8  `json:"deviceBitmapYMin"`
	DeviceBitmapYMax           uint8  `json:"deviceBitmapYMax"`
	DeviceBitmapYSize          uint8  `json:"deviceBitmapYSize"`
	DeviceLinearMin            uint8  `json:"deviceLinearMin"`
	DeviceLinearMax            uint8  `json:"deviceLinearMax"`
	DeviceLinearSize           uint8  `json:"deviceLinearSize"`
	AdjustedX                  uint16 `json:"adjustedX"`
	AdjustedY                  uint16 `json:"adjustedY"`

	src devicert.PrismZone
}

func (z *zone) pos() point { return point{int(z.AdjustedX), int(z.AdjustedY)} }

// device is device_service_types.CachePrismSyncDevice (with its embedded
// coordinate info, adjustment and bitmap/linear metadata flattened as the
// Windows JSON has them).
type device struct {
	Zones                []zone `json:"zones"`
	ID                   int    `json:"id"`
	HasBitmapCoordinates bool   `json:"hasBitmapCoordinates"`
	HasLinearIndexing    bool   `json:"hassLinearIndexing"` // sic
	HasActuations        bool   `json:"hasActuations"`
	Type                 int    `json:"type"`
	Name                 string `json:"name"`
	FullName             string `json:"fullName"`
	Region               string `json:"region"`
	CoordinatePlane      int    `json:"coordinatePlane"`
	XAdjustment          int32  `json:"xAdjustment"`
	YAdjustment          int32  `json:"yAdjustment"`
	CanvasTranslationX   int    `json:"canvasTranslationX"`
	CanvasTranslationY   int    `json:"canvasTranslationY"`
	Enabled              bool   `json:"enabled"`
	FirstTimeConnection  bool   `json:"firstTimeConnection"`
	BitmapXMin           uint8  `json:"bitmapXMin"`
	BitmapXMax           uint8  `json:"bitmapXMax"`
	BitmapXSize          uint8  `json:"bitmapXSize"`
	BitmapYMin           uint8  `json:"bitmapYMin"`
	BitmapYMax           uint8  `json:"bitmapYMax"`
	BitmapYSize          uint8  `json:"bitmapYSize"`
	LinearMin            uint8  `json:"linearMin"`
	LinearMax            uint8  `json:"linearMax"`
	LinearSize           uint8  `json:"linearSize"`

	productID uint32
	stepMs    int
	writes    []devicert.PrismWrite // frame template
	init      []devicert.PrismWrite
}

// zoneUniqueID is generateZoneUniqueID: the device id, the zone's
// position and a counter for zones sharing a position, packed in 64 bits.
func zoneUniqueID(deviceID int, x, y uint16, seen map[uint64]uint16) string {
	key := uint64(deviceID)<<48 | uint64(x)<<32 | uint64(y)<<16
	n := seen[key]
	seen[key] = n + 1
	return strconv.FormatUint(key|uint64(n), 10)
}

// planeConfig is coordinates.CoordinatePlaneConfig.
type planeConfig struct {
	XStart, XEnd int
	XReversed    bool
	YStart, YEnd int
	YReversed    bool
}

// The coordinate planes (the desk, the wall behind it above and below,
// the sides...) PrismSync places devices on, by the plane each spec
// names.
var planes = map[int]planeConfig{
	0: {31000, 33500, false, 30000, 31000, false},
	1: {31000, 33500, false, 30600, 34000, false},
	2: {25000, 31000, true, 31000, 34000, false},
	3: {33500, 39500, false, 31000, 34000, false},
	4: {31000, 33500, false, 28500, 31000, false},
	5: {25000, 31000, false, 28500, 31000, false},
	6: {33500, 39500, false, 28500, 31000, false},
	7: {31000, 33500, false, 21500, 28500, true},
	8: {31000, 33500, false, 34000, 40000, false},
}

// Where devices of a type sit on their plane by default (mice right of
// the keyboard, or left for the left-handed).
type typeHand struct {
	deviceType int
	leftHanded bool
}

var defaultAdjustments = map[typeHand][2]int32{
	{0, true}:  {180, 0},
	{1, false}: {1095, 50},
	{1, true}:  {85, 50},
	{7, false}: {1095, 50},
	{3, false}: {280, 0},
	{3, true}:  {460, 0},
	{10, true}: {180, 0},
}

// Canvas units are plane units divided by 2.2, y pointing up.
const canvasScale = 2.2

// place sets the device's zones' plane positions and its canvas
// translation (CalculateDeviceTotalAdjustment and
// ApplyCalculatedCanvasTranslations).
func (d *device) place() {
	pl, ok := planes[d.CoordinatePlane]
	if !ok {
		pl = planes[0]
	}
	baseX, baseY := pl.XStart, pl.YStart
	if pl.XReversed {
		maxX := 0
		for _, z := range d.Zones {
			maxX = max(maxX, int(z.X))
		}
		baseX = pl.XEnd - maxX
	}
	if pl.YReversed {
		maxY := 0
		for _, z := range d.Zones {
			maxY = max(maxY, int(z.Y))
		}
		baseY = pl.YEnd - maxY
	}
	baseX += int(d.XAdjustment)
	baseY += int(d.YAdjustment)
	minX, minY := 0xffff, 0xffff
	for i := range d.Zones {
		z := &d.Zones[i]
		z.AdjustedX = uint16(min(max(int(z.X)+baseX, 0), 0xffff))
		z.AdjustedY = uint16(min(max(int(z.Y)+baseY, 0), 0xffff))
		minX, minY = min(minX, int(z.AdjustedX)), min(minY, int(z.AdjustedY))
	}
	d.CanvasTranslationX = int(math.Ceil(float64(minX-31000) / canvasScale))
	d.CanvasTranslationY = int(math.Ceil(float64(30000-minY) / canvasScale))
}

// newDevice turns an Engine device into PrismSync's view of it.
func newDevice(e engine.PrismDevice, adj *[2]int32, leftHanded bool) *device {
	p := e.Data
	d := &device{ID: e.ID, Type: e.Type, Name: e.Name, FullName: e.FullName, Region: p.Region,
		HasBitmapCoordinates: p.HasBitmap, HasLinearIndexing: p.HasLinear, CoordinatePlane: p.CoordinatePlane,
		Enabled: true, productID: e.ProductID, stepMs: p.StepMs, writes: p.ZoneWrites, init: p.InitWrites}
	if d.Region == "" {
		d.Region = "us"
	}
	if adj != nil {
		d.XAdjustment, d.YAdjustment = adj[0], adj[1]
	} else if a, ok := defaultAdjustments[typeHand{e.Type, leftHanded}]; ok {
		d.XAdjustment, d.YAdjustment = a[0], a[1]
	}
	seen := map[uint64]uint16{}
	first := true
	for _, z := range p.Zones {
		nz := zone{X: z.X, Y: z.Y, BitmapX: z.BitmapX, BitmapY: z.BitmapY, HidCode: z.HidCode, LinearIndex: z.LinearIndex,
			DeviceID: e.ID, DeviceHasBitmapCoordinates: p.HasBitmap, DeviceHasLinearIndexing: p.HasLinear, DeviceType: e.Type, src: z}
		nz.UniqueID = zoneUniqueID(e.ID, z.X, z.Y, seen)
		if first {
			d.BitmapXMin, d.BitmapXMax, d.BitmapYMin, d.BitmapYMax = z.BitmapX, z.BitmapX, z.BitmapY, z.BitmapY
			d.LinearMin, d.LinearMax = z.LinearIndex, z.LinearIndex
			first = false
		}
		d.BitmapXMin, d.BitmapXMax = min(d.BitmapXMin, z.BitmapX), max(d.BitmapXMax, z.BitmapX)
		d.BitmapYMin, d.BitmapYMax = min(d.BitmapYMin, z.BitmapY), max(d.BitmapYMax, z.BitmapY)
		d.LinearMin, d.LinearMax = min(d.LinearMin, z.LinearIndex), max(d.LinearMax, z.LinearIndex)
		d.Zones = append(d.Zones, nz)
	}
	if p.HasBitmap {
		d.BitmapXSize, d.BitmapYSize = d.BitmapXMax-d.BitmapXMin+1, d.BitmapYMax-d.BitmapYMin+1
	}
	if p.HasLinear {
		d.LinearSize = d.LinearMax - d.LinearMin + 1
	}
	for i := range d.Zones {
		z := &d.Zones[i]
		z.DeviceBitmapXMin, z.DeviceBitmapXMax, z.DeviceBitmapXSize = d.BitmapXMin, d.BitmapXMax, d.BitmapXSize
		z.DeviceBitmapYMin, z.DeviceBitmapYMax, z.DeviceBitmapYSize = d.BitmapYMin, d.BitmapYMax, d.BitmapYSize
		z.DeviceLinearMin, z.DeviceLinearMax, z.DeviceLinearSize = d.LinearMin, d.LinearMax, d.LinearSize
	}
	d.place()
	return d
}

// frame renders the device's frame from zone colours: a copy of its
// report template with each zone's colour written in.
func (d *device) frame(colors map[string]Color) []devicert.PrismWrite {
	out := make([]devicert.PrismWrite, len(d.writes))
	for i, w := range d.writes {
		w.Data = append([]byte(nil), w.Data...)
		out[i] = w
	}
	for _, z := range d.Zones {
		c := colors[z.UniqueID]
		w := out[z.src.Write].Data
		w[z.src.Red], w[z.src.Green], w[z.src.Blue] = c.Red, c.Green, c.Blue
	}
	return out
}
