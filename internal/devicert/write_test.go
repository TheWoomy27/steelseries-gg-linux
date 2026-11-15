package devicert

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steelseries/golisp"
)

type sentChunk struct {
	data []byte
	read bool
}

type recordingTransport struct {
	sent   []sentChunk
	asleep bool // reads time out, as a wireless mouse asleep behind its dongle
}

func (r *recordingTransport) Chunk(_ Target, _, _ int, data []byte, read bool, _, _ time.Duration) ([]byte, error) {
	r.sent = append(r.sent, sentChunk{append([]byte{}, data...), read})
	if read && r.asleep {
		return nil, errors.New("timed out")
	}
	if read {
		return []byte{0, 0x2b, 0x40}, nil
	}
	return nil, nil
}

func (r *recordingTransport) Connected(Target) bool { return true }

const writeTestSpec = `
(device 0x10380001
    (usage-page 0xffc0)
    (usage 0x0001)

    (struct dimmer
        (unaligned)
        (field report_id uint8 (constant 0x00))
        (field command uint8 (constant 0x42))
        (field value uint8))
    (api dimmer
        (api-write HID (chunk HIDIO 65 payload)))

    (struct polling_rate
        (unaligned)
        (field report_id uint8 (constant 0x00))
        (field command uint8 (constant 0x2b))
        (field level uint8))
    (api polling_rate
        (mixes-reads-writes)
        (api-write HID (write-chunk HIDIO 65 [0x00 0x01]) (read-chunk HIDIO 65 payload)))

    (define (rewrite) (rewrite-cached-values 0x10380001 polling_rate))
    (define (rewrite-dimmer) (rewrite-cached-values 0x10380001 dimmer))

    (define (macros)
        (convert-onboard-macro-bindings 1 "" (list 1 2) (list 3 4) 8 0x71)))
`

func writeTestDevice(t *testing.T) (*Decl, *recordingTransport) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.lsp"), []byte(writeTestSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &recordingTransport{}
	rt, err := New(dir, dir, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := rt.Device(0x10380001)
	if err != nil {
		t.Fatal(err)
	}
	return d, tr
}

// Engine's DeviceLib only reads a reply when an api is read, or for the
// read-chunks of an api that mixes reads and writes. Devices such as the
// legacy Apex keyboards never answer a write.
func TestWriteChunksReadOnlyWhenAsked(t *testing.T) {
	d, tr := writeTestDevice(t)
	if err := d.WriteApi("dimmer", map[string]any{"value": 255}); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteApi("polling_rate", map[string]any{"level": 2}); err != nil {
		t.Fatal(err)
	}
	want := []sentChunk{
		{[]byte{0x00, 0x42, 0xff}, false},
		{[]byte{0x00, 0x01}, false},
		{[]byte{0x00, 0x2b, 0x02}, true},
	}
	if len(tr.sent) != len(want) {
		t.Fatalf("sent %d chunks, want %d", len(tr.sent), len(want))
	}
	for i, w := range want {
		if got := tr.sent[i]; string(got.data) != string(w.data) || got.read != w.read {
			t.Errorf("chunk %d: sent % x (read %v), want % x (read %v)", i, got.data, got.read, w.data, w.read)
		}
	}
}

func TestConvertOnboardMacroBindingsKeepsMappings(t *testing.T) {
	d, _ := writeTestDevice(t)
	v, err := d.Call("macros")
	if err != nil {
		t.Fatal(err)
	}
	if got := golisp.String(golisp.Car(v)); got != "(1 2)" {
		t.Errorf("mappings: %s, want (1 2)", got)
	}
	if got := bytesOf(golisp.Cadr(v)); len(got) != 8 || string(got) != string(make([]byte, 8)) {
		t.Errorf("macro data: % x, want 8 zero bytes", got)
	}
}

// A setting the device did not take (a wireless mouse asleep) is still
// what the spec rewrites when the device connects again; an api nothing
// was written to is left alone.
func TestRewriteCachedValues(t *testing.T) {
	d, tr := writeTestDevice(t)
	tr.asleep = true
	if err := d.WriteApi("polling_rate", map[string]any{"level": 3}); err == nil {
		t.Fatal("the write should fail while the device sleeps")
	}
	if len(d.CachedValues("polling_rate")) != 0 {
		t.Errorf("a failed write was cached: %v", d.CachedValues("polling_rate"))
	}
	tr.asleep, tr.sent = false, nil
	if v, err := d.Call("rewrite"); err != nil || !golisp.BooleanValue(v) {
		t.Fatalf("rewrite: %v %v", golisp.String(v), err)
	}
	if n := len(tr.sent); n != 2 || string(tr.sent[1].data) != string([]byte{0x00, 0x2b, 0x03}) {
		t.Errorf("rewrite sent %v, want the polling rate at level 3", tr.sent)
	}
	tr.sent = nil
	if v, err := d.Call("rewrite-dimmer"); err != nil || golisp.BooleanValue(v) || len(tr.sent) != 0 {
		t.Errorf("rewriting an api never written sent %v (%v %v)", tr.sent, golisp.String(v), err)
	}
}

type rotationHooks struct {
	nopHooks
	dev     uint32
	on      bool
	degrees float64
}

func (h *rotationHooks) MouseRotation(dev uint32, on bool, degrees float64) {
	h.dev, h.on, h.degrees = dev, on, degrees
}

// The Sensei Pro's engine_settings sends its rotation to GG's driver
// (an SSE chunk) and to the mouse; ggd gets the driver's part.
func TestMouseRotationToDriver(t *testing.T) {
	const spec = `
(device 0x10380002
    (usage-page 0xffc0)
    (usage 0x0001)
    (struct engine_settings
        (unaligned)
        (field report_id uint8 (constant 0x00))
        (field command uint8 (constant 0x17))
        (field mouse_rotation_enabled uint8 (values 0 1))
        (field mouse_rotation_angle float32 (range -180 180)))
    (api engine_settings
        (api-write SSE (chunk MOUSEROTATION 5 [
            (extract-bytes payload 3 4)
            (extract-byte payload 2)
        ]))
        (api-write HID (chunk HIDIO 65 payload))))
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.lsp"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &recordingTransport{}
	h := &rotationHooks{}
	rt, err := New(dir, dir, tr, h)
	if err != nil {
		t.Fatal(err)
	}
	d, err := rt.Device(0x10380002)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.WriteApi("engine_settings", map[string]any{"mouse_rotation_enabled": 1, "mouse_rotation_angle": -12.5}); err != nil {
		t.Fatal(err)
	}
	if h.dev != 0x10380002 || !h.on || h.degrees != -12.5 {
		t.Errorf("rotation: %+v", h)
	}
	if len(tr.sent) != 1 {
		t.Errorf("%d HID chunks, want 1 (the mouse's copy)", len(tr.sent))
	}
}

type timedTransport struct {
	at   []time.Time
	sent [][]byte
}

func (r *timedTransport) Chunk(_ Target, _, _ int, data []byte, _ bool, _, _ time.Duration) ([]byte, error) {
	r.at = append(r.at, time.Now())
	r.sent = append(r.sent, append([]byte{}, data...))
	return nil, nil
}

func (r *timedTransport) Connected(Target) bool { return true }

// Three one-chunk apis, as a deploy sends them to the Apex Pro (2024):
// meta_toggle_hid, the OLED picture, rapid_tap_enable.
const spacingSpec = `
(device 0x10380003
    (usage-page 0xffc0)
    (usage 0x0001)
    (define (time-between-commands) 40)

    (struct meta_toggle_hid
        (unaligned)
        (field report_id uint8 (constant 0x00))
        (field command uint8 (constant 0x60))
        (field code uint8))
    (api meta_toggle_hid
        (api-write HID (chunk HIDIO 65 payload)))

    (struct oled_display
        (unaligned)
        (field report_id uint8 (constant 0x00))
        (field command uint8 (constant 0x65))
        (field pixel uint8))
    (api oled_display
        (api-write HID (chunk HIDFEATURE 643 payload)))

    (struct rapid_tap_enable
        (unaligned)
        (field report_id uint8 (constant 0x00))
        (field command uint8 (constant 0x1A))
        (field enabled uint8 (values 0 1)))
    (api rapid_tap_enable
        (api-write HID (chunk HIDIO 65 payload))))
`

func spacingTestDevice(t *testing.T) (*Decl, *timedTransport) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.lsp"), []byte(spacingSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &timedTransport{}
	rt, err := New(dir, dir, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := rt.Device(0x10380003)
	if err != nil {
		t.Fatal(err)
	}
	return d, tr
}

func checkSpacing(t *testing.T, tr *timedTransport, n int, gap time.Duration) {
	t.Helper()
	if len(tr.at) != n {
		t.Fatalf("sent %d chunks, want %d", len(tr.at), n)
	}
	for i := 1; i < n; i++ {
		if d := tr.at[i].Sub(tr.at[i-1]); d < gap {
			t.Errorf("chunk %d (% x) went %v after the one before, want at least %v", i, tr.sent[i], d, gap)
		}
	}
}

// DeviceLib keeps a device's time between commands between every two
// commands, not only between the chunks of one api. A deploy writes its
// apis back to back; the Apex Pro (2024) lost the OLED picture when the
// next api followed it 3 ms later.
func TestTimeBetweenCommandsAcrossApis(t *testing.T) {
	d, tr := spacingTestDevice(t)
	if err := d.WriteApi("meta_toggle_hid", map[string]any{"code": 0xf0}); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteApi("oled_display", map[string]any{"pixel": 0x80}); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteApi("rapid_tap_enable", map[string]any{"enabled": 0}); err != nil {
		t.Fatal(err)
	}
	checkSpacing(t, tr, 3, 40*time.Millisecond)
	if got := tr.sent[1]; len(got) < 3 || got[1] != 0x65 || got[2] != 0x80 {
		t.Errorf("second chunk % x, want the OLED picture", got)
	}
}

// PrismSync frames go through the same executor: a frame right after a
// deploy's last api waits its turn too.
func TestTimeBetweenCommandsApiThenPrism(t *testing.T) {
	d, tr := spacingTestDevice(t)
	if err := d.WriteApi("oled_display", map[string]any{"pixel": 1}); err != nil {
		t.Fatal(err)
	}
	if err := d.WritePrism([]PrismWrite{{Type: HIDFeature, Size: 643, Data: []byte{0x00, 0x3a, 0x01}}}); err != nil {
		t.Fatal(err)
	}
	checkSpacing(t, tr, 2, 40*time.Millisecond)
}
