package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ggl/internal/devicert"
	"ggl/internal/events"
	"ggl/internal/store"
)

// syncEngine is an Engine with a device's current configuration.
func syncEngine(t *testing.T, settings string) *Engine {
	e := bindingsEngine(t)
	if _, err := e.db.Exec(`CREATE TABLE configurations (id UUID PRIMARY KEY UNIQUE, device_id INTEGER NOT NULL, name TEXT, settings TEXT,
		active INTEGER DEFAULT '0' NOT NULL, user_id INTEGER NOT NULL, mutable INTEGER DEFAULT '1' NOT NULL, last_deployed integer default (0) not null,
		undeployedsettings TEXT DEFAULT "{}" NOT NULL, cloud_last_updated_at string DEFAULT "", is_deleted integer default (0) not null,
		on_deploy_sse_command_id UUID DEFAULT 0, config_page_modal_localization_key TEXT NOT NULL DEFAULT '', extra_properties TEXT NOT NULL DEFAULT '{}',
		sync_config INT DEFAULT 0, is_quickset_auto_deletion_ok INTEGER NOT NULL DEFAULT(0), quickset_auto_created_for_profile TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO configurations (id, device_id, name, settings, user_id, last_deployed) VALUES ('c1', 1, 'Default', ?, 0, 1)`, settings); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "engine.json"))
	if err != nil {
		t.Fatal(err)
	}
	e.state, e.hub, e.broadcast = st, events.NewHub("test", ""), func(string, any) {}
	return e
}

func (e *Engine) settingsOf(t *testing.T, id string) map[string]any {
	c, err := e.configByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return c.settingsMap()
}

// Rapid trigger turned on for some keys from an Apex keyboard's own menu
// (its sync report 0x36) is kept in GG's configuration, so writing the
// configuration again (the keyboard waking, ggd starting) keeps it.
func TestSyncDataToConfig(t *testing.T) {
	e := syncEngine(t, `{"release_mode":{"keys":[{"hid":4,"mode":0},{"hid":5,"mode":0},{"hid":6,"mode":0}]},"oled":{"brightness":3}}`)
	l := &liveDevice{dbID: 1, physical: map[uint32]*devicert.Decl{}}
	// As the spec's update_config_settings sends it: every key, the ones
	// not changed with their hid alone.
	var ev map[string]any
	json.Unmarshal([]byte(`{"syncDataToConfig":{"configId":"","settings":{"release_mode":{"keys":[{"hid":4},{"hid":6,"mode":1},{"hid":5}]}}}}`), &ev)
	s := ev["syncDataToConfig"].(map[string]any)
	e.syncToConfig(l, s["configId"].(string), s["settings"].(map[string]any))
	got, _ := json.Marshal(e.settingsOf(t, "c1"))
	want := `{"oled":{"brightness":3},"release_mode":{"keys":[{"hid":4,"mode":0},{"hid":5,"mode":0},{"hid":6,"mode":1}]}}`
	if string(got) != want {
		t.Errorf("configuration %s\nwant %s", got, want)
	}
}

func TestMergeSettingByPosition(t *testing.T) {
	cur := map[string]any{"index": 1.0, "mouse_sensitivity": []any{map[string]any{"x_dpi": 400.0}, map[string]any{"x_dpi": 800.0}}}
	got, _ := json.Marshal(mergeSetting(cur, map[string]any{"index": 0.0, "mouse_sensitivity": []any{map[string]any{"x_dpi": 500.0}}}))
	if want := `{"index":0,"mouse_sensitivity":[{"x_dpi":500},{"x_dpi":800}]}`; string(got) != want {
		t.Errorf("merged %s, want %s", got, want)
	}
}

const syncSpec = `
(device 0x10380005
    (usage-page 0xffc0)
    (usage 0x0001)
    (struct dpi
        (unaligned)
        (field report_id uint8 (constant 0x00))
        (field command uint8 (constant 0x34))
        (field x_dpi uint16))
    (api dpi
        (api-write HID (chunk HIDIO 65 payload)))
    (define (sync_settings_function) "{\"dpi\": {\"x_dpi\": 800}, \"lan_mode\": {\"enabled\": 1}}"))
`

type sentTransport struct{ sent [][]byte }

func (s *sentTransport) Chunk(_ devicert.Target, _, _ int, data []byte, _ bool, _, _ time.Duration) ([]byte, error) {
	s.sent = append(s.sent, append([]byte{}, data...))
	return nil, nil
}

func (s *sentTransport) Connected(devicert.Target) bool { return true }

// A Sensei Pro back from a battery swap reports its default 800 DPI. GG
// manages it, so its configuration (500) stays and is written again,
// where it used to take the 800 and write nothing.
func TestReconnectKeepsManagedConfig(t *testing.T) {
	e := syncEngine(t, `{"dpi":{"x_dpi":500}}`)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.lsp"), []byte(syncSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &sentTransport{}
	rt, err := devicert.New(dir, dir, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := rt.Device(0x10380005)
	if err != nil {
		t.Fatal(err)
	}
	l := &liveDevice{dbID: 1, cat: &catalogDevice{Name: "mouse", FullName: "Mouse"}, physical: map[uint32]*devicert.Decl{d.ID: d}}
	e.markManaged(1)
	d.SetCachedValues("dpi", map[string]any{"x_dpi": 500}) // written before the swap

	s, err := d.SyncSettings()
	if err != nil {
		t.Fatal(err)
	}
	e.noteDeviceSettings(l, d, s)
	settings := e.settingsOf(t, "c1")
	if dpi := settings["dpi"].(map[string]any)["x_dpi"]; dpi != 500.0 {
		t.Errorf("configuration has %v DPI, want 500", dpi)
	}
	if _, ok := settings["lan_mode"]; !ok {
		t.Error("a setting the configuration didn't have was not kept")
	}
	if err := e.deploy(l, settings, false, false); err != nil {
		t.Fatal(err)
	}
	if len(tr.sent) != 1 || tr.sent[0][2] != 0xf4 || tr.sent[0][3] != 0x01 {
		t.Errorf("wrote %x, want the 500 DPI (f401)", tr.sent)
	}
}
