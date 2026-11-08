package devicert

import (
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

type recordingTransport struct{ sent []sentChunk }

func (r *recordingTransport) Chunk(_ Target, _, _ int, data []byte, read bool, _, _ time.Duration) ([]byte, error) {
	r.sent = append(r.sent, sentChunk{append([]byte{}, data...), read})
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
