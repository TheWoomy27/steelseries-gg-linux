package engine

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"ggl/internal/devicert"
)

// config is a row of the configurations table (domains.Configuration).
type config struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Active             int    `json:"active"`
	Settings           string `json:"settings"`
	UndeployedSettings string `json:"undeployedSettings"`
	DeviceID           int    `json:"device_id"`
	LastDeployed       int64  `json:"last_deployed"`
	Mutable            int    `json:"mutable"`
	CloudLastUpdatedAt string `json:"cloud_last_updated_at"`
	IsDeleted          int    `json:"is_deleted"`
	OnDeploySseCmdID   string `json:"on_deploy_sse_command_id"`
	ConfigPageModalKey string `json:"config_page_modal_localization_key"`
	ExtraProperties    string `json:"extra_properties"`
	SyncConfig         int    `json:"sync_config"`
	OnboardProfileSlot int    `json:"onboard_profile_slot"`
	QuicksetDeleteOK   int    `json:"is_quickset_auto_deletion_ok"`
	QuicksetFor        string `json:"quickset_auto_created_for_profile"`
	Applications       []any  `json:"applications"`
	Bindings           []any  `json:"bindings"`
	UniversalTriggers  []any  `json:"universal_triggers"`
}

func (c *config) settingsMap() map[string]any {
	m := map[string]any{}
	json.Unmarshal([]byte(c.Settings), &m)
	return m
}

const configCols = `id, coalesce(name,''), active, coalesce(settings,'{}'), undeployedsettings, device_id, last_deployed, mutable,
	cloud_last_updated_at, is_deleted, coalesce(on_deploy_sse_command_id,''), config_page_modal_localization_key, extra_properties,
	coalesce(sync_config,0), is_quickset_auto_deletion_ok, quickset_auto_created_for_profile`

func scanConfig(row interface{ Scan(...any) error }) (*config, error) {
	var c config
	var sse any
	err := row.Scan(&c.ID, &c.Name, &c.Active, &c.Settings, &c.UndeployedSettings, &c.DeviceID, &c.LastDeployed, &c.Mutable,
		&c.CloudLastUpdatedAt, &c.IsDeleted, &sse, &c.ConfigPageModalKey, &c.ExtraProperties, &c.SyncConfig, &c.QuicksetDeleteOK, &c.QuicksetFor)
	if err != nil {
		return nil, err
	}
	if s, ok := sse.(string); ok && s != "0" {
		c.OnDeploySseCmdID = s
	}
	c.Applications, c.Bindings, c.UniversalTriggers = []any{}, []any{}, []any{}
	return &c, nil
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (e *Engine) configsForDevice(deviceID int) ([]*config, error) {
	rows, err := e.db.Query(`SELECT `+configCols+` FROM configurations WHERE device_id = ? AND user_id = ? AND is_deleted = 0`, deviceID, e.userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*config
	for rows.Next() {
		c, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, rows.Err()
}

func (e *Engine) configByID(id string) (*config, error) {
	return scanConfig(e.db.QueryRow(`SELECT `+configCols+` FROM configurations WHERE id = ?`, id))
}

func (e *Engine) activeConfig(deviceID int) (*config, error) {
	return scanConfig(e.db.QueryRow(`SELECT `+configCols+` FROM configurations WHERE device_id = ? AND user_id = ? AND active = 1 AND is_deleted = 0 LIMIT 1`, deviceID, e.userID))
}

func (e *Engine) configMetadata(deviceID int) []map[string]any {
	cfgs, _ := e.configsForDevice(deviceID)
	out := []map[string]any{}
	for _, c := range cfgs {
		out = append(out, map[string]any{
			"id": c.ID, "name": c.Name, "device_id": c.DeviceID, "last_deployed": c.LastDeployed, "active": c.Active,
			"is_quickset_auto_deletion_ok": c.QuicksetDeleteOK, "quickset_auto_created_for_profile": c.QuicksetFor,
		})
	}
	return out
}

// ensureConfig gives a newly seen device its initial configurations, the
// same presets the Windows Engine creates on first plug-in.
func (e *Engine) ensureConfig(cat *catalogDevice) {
	cfgs, err := e.configsForDevice(cat.ID)
	if err != nil || len(cfgs) > 0 {
		return
	}
	type preset struct{ name, settings string }
	var presets []preset
	rows, err := e.db.Query(`SELECT name, settings FROM initial_configurations WHERE device_id = ? ORDER BY id`, cat.ID)
	if err == nil {
		for rows.Next() {
			var p preset
			rows.Scan(&p.name, &p.settings)
			presets = append(presets, p)
		}
		rows.Close()
	}
	if len(presets) == 0 {
		presets = []preset{{"Default", cat.Settings}}
	}
	for i, p := range presets {
		active := 0
		if i == 0 {
			active = 1
		}
		if _, err := e.createConfig(cat.ID, p.name, p.settings, active, 0); err != nil {
			slog.Warn("create initial configuration", "device", cat.Name, "err", err)
		}
	}
}

func (e *Engine) createConfig(deviceID int, name, settings string, active int, lastDeployed int64) (*config, error) {
	id := newUUID()
	if _, err := e.db.Exec(`INSERT INTO configurations (id, device_id, name, settings, active, user_id, mutable, last_deployed)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?)`, id, deviceID, name, settings, active, e.userID, lastDeployed); err != nil {
		return nil, err
	}
	return e.configByID(id)
}

func (e *Engine) saveConfig(c *config) error {
	_, err := e.db.Exec(`UPDATE configurations SET name = ?, settings = ?, undeployedsettings = ?, extra_properties = ? WHERE id = ?`,
		c.Name, c.Settings, c.UndeployedSettings, c.ExtraProperties, c.ID)
	return err
}

func (e *Engine) setActive(c *config) {
	e.db.Exec(`UPDATE configurations SET active = 0 WHERE device_id = ? AND user_id = ?`, c.DeviceID, e.userID)
	e.db.Exec(`UPDATE configurations SET active = 1, last_deployed = ? WHERE id = ?`, time.Now().Unix(), c.ID)
}

// DeployConfigByID activates a configuration and writes it to its device
// (used by GG loadouts).
func (e *Engine) DeployConfigByID(id string) {
	c, err := e.configByID(id)
	if err != nil {
		return
	}
	e.deployConfig(c, false)
}

func (e *Engine) deployConfig(c *config, force bool) error {
	e.setActive(c)
	l := e.liveByID(c.DeviceID)
	if l == nil {
		return nil // applied on next connect
	}
	err := e.deploy(l, c.settingsMap(), force, l.cat.PersistChanges)
	e.emit("switchConfig", map[string]any{"deviceId": c.DeviceID, "configId": c.ID})
	e.emit("reloadConfigs", map[string]any{"id": c.DeviceID})
	return err
}

// deploy writes configuration settings through the device's APIs. Each
// top-level key names an api (or a lighting sequence); values are that
// api's struct fields. Unchanged apis are skipped unless force is set.
// saveToFlash is passed to the spec's (post_deploy save-to-flash), which
// persists settings to onboard memory on devices that have it.
func (e *Engine) deploy(l *liveDevice, settings map[string]any, force, saveToFlash bool) error {
	d := l.primary()
	if d == nil {
		return fmt.Errorf("%s has no connected interface", l.cat.FullName)
	}
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var errs []string
	for _, k := range keys {
		if err := e.writeSetting(d, k, settings[k], force || d.AlwaysWriteAllSettings); err != nil {
			errs = append(errs, k+": "+err.Error())
		}
	}
	if err := d.PostDeploy(saveToFlash); err != nil {
		slog.Debug("post_deploy", "device", d.Name, "err", err)
	}
	if len(errs) > 0 {
		slog.Warn("deploy finished with errors", "device", l.cat.Name, "errors", strings.Join(errs, "; "))
		return fmt.Errorf("%d settings failed: %s", len(errs), strings.Join(errs, "; "))
	}
	return nil
}

func (e *Engine) writeSetting(d *devicert.Decl, key string, value any, force bool) error {
	if api, seq := d.SequenceForSetting(key); api != "" {
		return e.sequencer.Play(d, api, seq, value)
	}
	if d.Api(key) == nil {
		return nil // keys handled elsewhere (bindings, UI-only state)
	}
	m, _ := value.(map[string]any)
	if !force && m != nil && sameValues(d.CachedValues(key), m) {
		return nil
	}
	err := d.WriteApi(key, value)
	if err == devicert.ErrFirmwareDisabled {
		return nil
	}
	return err
}

func sameValues(cached, want map[string]any) bool {
	if cached == nil {
		return false
	}
	a, _ := json.Marshal(normalizeNumbers(cached))
	b, _ := json.Marshal(normalizeNumbers(want))
	return string(a) == string(b)
}

func normalizeNumbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = normalizeNumbers(e)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, e := range x {
			a[i] = normalizeNumbers(e)
		}
		return a
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return v
}

var errNotFound = sql.ErrNoRows
