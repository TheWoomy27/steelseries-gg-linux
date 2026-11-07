package devicert

import (
	"sync"

	"github.com/steelseries/golisp"
)

// Decl mirrors support.DeviceDeclaration.
type Decl struct {
	rt   *Runtime
	ID   uint32
	Name string

	UsagePage    uint16
	Usage        uint16
	InterfaceNum int

	HasSync       bool
	SyncUsagePage uint16
	SyncUsage     uint16
	SyncNum       int

	Env      *golisp.SymbolTableFrame
	Structs  map[string]*Struct
	Apis     map[string]*Api
	Init     *golisp.Data
	Shutdown *golisp.Data

	SupportsDirectWrite       bool
	SupportsSingleDirectWrite bool
	SupportsKeyBlink          bool
	SupportsPlayVibration     bool
	SupportsOnboardProfiles   bool
	SupportsPrismSync         bool
	PrismSyncV2Whitelisted    bool
	PausePrismSyncOnDeploy    bool
	AlwaysWriteAllSettings    bool
	AllowDisablingDeployment  bool
	NotBootloader             bool
	HideInTaskbarContextMenu  bool
	DeviceCardApp             string
	BackgroundSoftwareID      string
	BackgroundSoftwareVersion string
	SequenceStepMs            int
	StartDelayMs              int

	CommandInterfaceEventType int64
	CommandInterfaceEventFunc *golisp.Data

	mu                 sync.Mutex
	cache              map[string]map[string]any
	blocked            bool
	firmwareSupported  *bool
	outputOnboardSchem int64
	syncDisabled       bool
	sequences          map[string]*Sequence
}

// Api mirrors support.DeviceApi.
type Api struct {
	Name              string
	Env               *golisp.SymbolTableFrame
	Read              *ApiCommand
	Write             []*ApiCommand
	Let               *golisp.Data
	LetStar           bool
	OnReadSuccess     *golisp.Data
	OnWriteSuccess    *golisp.Data
	OnReadFailure     *golisp.Data
	OnWriteFailure    *golisp.Data
	Finally           *golisp.Data
	CacheExempt       bool
	MixesReadsWrites  bool
	HoldAfterWriteMs  int
	IsFirmwareUpdate  bool
	Unblockable       bool
	NoPreempt         bool
	ReadChunksSpecFn  *golisp.Data
	ReadbackErrByte   *[2]int64 // position, value
	SequenceTemplate  string
	SequenceName      string
	sequenceStartup   *golisp.Data
	sequenceShutdown  *golisp.Data
	derivedFrom       string
	declaredInDecl    *Decl
	readSpecifiersRaw *golisp.Data
}

// ApiCommand is one (api-read PROTOCOL ...) or (api-write PROTOCOL ...).
type ApiCommand struct {
	Protocol int64
	Groups   []*ChunkGroup
}

// ChunkGroup is a run of chunks, optionally repeated by (chunk-loop N ...).
type ChunkGroup struct {
	Chunks    []*Chunk
	LoopCount *golisp.Data
}

// Chunk is one (chunk TYPE SIZE code) / (read-chunk ...) / (write-chunk ...).
type Chunk struct {
	Type       int64
	Size       int64
	Code       *golisp.Data
	SetReadBit bool // read-chunk
	WriteOnly  bool // write-chunk
}

// Sequence is a lighting animation template bound to an api.
type Sequence struct {
	Api      string
	Template string
	Name     string
}

func (d *Decl) Struct(name string) *Struct {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.Structs[name]
}

func (d *Decl) Api(name string) *Api {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.Apis[name]
}

// ApiNames lists the declared APIs.
func (d *Decl) ApiNames() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.Apis))
	for k := range d.Apis {
		out = append(out, k)
	}
	return out
}

// CachedValues returns the last values written to or read from an api.
func (d *Decl) CachedValues(api string) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return copyMap(d.cache[api])
}

func (d *Decl) setCache(api string, v map[string]any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cache == nil {
		d.cache = map[string]map[string]any{}
	}
	d.cache[api] = copyMap(v)
}

// BustCache forgets cached values so the next deploy writes everything.
func (d *Decl) BustCache() {
	d.mu.Lock()
	d.cache = nil
	d.mu.Unlock()
}

// FirmwareSupported is what (set-is-supported-version ...) recorded.
func (d *Decl) FirmwareSupported() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.firmwareSupported == nil || *d.firmwareSupported
}

func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// RunInit evaluates the device's (init ...) block, which reads firmware
// versions and brings the device into the state the Engine expects.
func (d *Decl) RunInit() error {
	if d.Init == nil {
		return nil
	}
	_, err := d.rt.evalBody(d.Init, d.Env)
	return err
}

// RunShutdown evaluates the device's (shutdown ...) block.
func (d *Decl) RunShutdown() error {
	if d.Shutdown == nil {
		return nil
	}
	_, err := d.rt.evalBody(d.Shutdown, d.Env)
	return err
}

// Connected reports whether the device's command interface is present.
func (d *Decl) Connected() bool { return d.rt.transport.Connected(d.target()) }

// SequenceForSetting maps a configuration key such as
// "aerox_5_wireless_z1_color_sequence" to the api that plays it.
func (d *Decl) SequenceForSetting(key string) (string, *Sequence) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for api, s := range d.sequences {
		if s.Name+"_sequence" == key || s.Template+"_sequence" == key {
			return api, s
		}
	}
	return "", nil
}
