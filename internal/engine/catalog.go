package engine

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// catalogDevice is a row of the Engine's devices table (seeded by the
// installer's migrations with every supported product).
type catalogDevice struct {
	ID                int
	ProductID         uint32
	Name              string
	FullName          string
	Type              int
	Settings          string
	Undeployed        string
	Hidden            bool
	AboutPageDisabled bool
	PersistChanges    bool
	DeploymentOff     bool
	HideDeviceCard    bool
	HasOnboardMacros  bool
	ModelTypeID       int
	GlobalSettings    string
}

const catalogCols = `id, product_id, name, full_name, type, settings, undeployedsettings, hidden, about_page_disabled,
	persist_changes, deployment_disabled, hide_device_card, has_onboard_macros, model_type_id, global_device_settings`

func scanCatalog(row interface{ Scan(...any) error }) (*catalogDevice, error) {
	var d catalogDevice
	var hidden, about, persist, deploy, hideCard, macros int
	err := row.Scan(&d.ID, &d.ProductID, &d.Name, &d.FullName, &d.Type, &d.Settings, &d.Undeployed, &hidden, &about,
		&persist, &deploy, &hideCard, &macros, &d.ModelTypeID, &d.GlobalSettings)
	if err != nil {
		return nil, err
	}
	d.Hidden, d.AboutPageDisabled, d.PersistChanges = hidden != 0, about != 0, persist != 0
	d.DeploymentOff, d.HideDeviceCard, d.HasOnboardMacros = deploy != 0, hideCard != 0, macros != 0
	return &d, nil
}

func (e *Engine) catalogByID(id int) (*catalogDevice, error) {
	return scanCatalog(e.db.QueryRow(`SELECT `+catalogCols+` FROM devices WHERE id = ?`, id))
}

func (e *Engine) catalogByProduct(pid uint32) (*catalogDevice, error) {
	return scanCatalog(e.db.QueryRow(`SELECT `+catalogCols+` FROM devices WHERE product_id = ?`, pid))
}

// mainProductFor maps a physical product (a dongle, a wired mouse...) to
// the logical device it belongs to.
func (e *Engine) mainProductFor(physical uint32) uint32 {
	var main uint32
	err := e.db.QueryRow(`SELECT main_device_product_id FROM devices_to_physical_devices WHERE physical_device_product_id = ? LIMIT 1`, physical).Scan(&main)
	if err != nil {
		return physical
	}
	return main
}

// wirelessInfo is how a wireless headset's transmitter (its dongle) pairs
// with its receiver (the headset, a device of its own when plugged in by
// USB), from the Engine's table, for the first of pids it lists. Device
// cards ask the receiver for its connection and firmware.
func (e *Engine) wirelessInfo(pids ...uint32) map[string]any {
	info := map[string]any{"transmitter_product_id": 0, "transmitter_name": "", "receiver_product_id": 0, "receiver_id": 0, "receiver_name": ""}
	for _, pid := range pids {
		var tx, rx, rxID int64
		var txName, rxName string
		err := e.db.QueryRow(`SELECT transmitter_product_id, transmitter_name, receiver_product_id, receiver_id, receiver_name
			FROM wireless_device_information WHERE transmitter_product_id = ?`, pid).Scan(&tx, &txName, &rx, &rxID, &rxName)
		if err == nil {
			info["transmitter_product_id"], info["transmitter_name"] = tx, txName
			info["receiver_product_id"], info["receiver_id"], info["receiver_name"] = rx, rxID, rxName
			break
		}
	}
	return info
}

type physicalRow struct {
	ProductID      uint32
	Name           string
	FullName       string
	Firmware       string
	BootloaderPID  uint32
	BootloaderMode bool
	IsExclusive    bool
	Priority       int
	CachedTypeID   int
}

func (e *Engine) physicalDevices(main uint32) ([]physicalRow, error) {
	rows, err := e.db.Query(`SELECT p.product_id, p.name, p.full_name, p.firmware_version, p.bootloader_product_id,
		p.bootloader_mode, p.is_exclusive, p.priority, p.cached_type_id
		FROM physical_devices p JOIN devices_to_physical_devices m ON m.physical_device_product_id = p.product_id
		WHERE m.main_device_product_id = ?`, main)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []physicalRow
	for rows.Next() {
		var p physicalRow
		var boot, excl int
		var bpid sql.NullInt64
		if err := rows.Scan(&p.ProductID, &p.Name, &p.FullName, &p.Firmware, &bpid, &boot, &excl, &p.Priority, &p.CachedTypeID); err != nil {
			return nil, err
		}
		p.BootloaderPID, p.BootloaderMode, p.IsExclusive = uint32(bpid.Int64), boot != 0, excl != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

type childRow struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	FullName  string `json:"full_name"`
	IsDefault int    `json:"is_default"`
	Offsets   string `json:"offsets"`
	IsCmf     int    `json:"is_cmf"`
}

func (e *Engine) childDevices(deviceID int) []childRow {
	rows, err := e.db.Query(`SELECT id, coalesce(name,''), coalesce(full_name,''), coalesce(is_default,0), coalesce(offsets,''), is_cmf FROM child_devices WHERE parent_device_id = ?`, deviceID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []childRow{}
	for rows.Next() {
		var c childRow
		if rows.Scan(&c.ID, &c.Name, &c.FullName, &c.IsDefault, &c.Offsets, &c.IsCmf) == nil {
			out = append(out, c)
		}
	}
	return out
}

type lightingTemplate struct {
	ID           string `json:"id"`
	EnglishLabel string `json:"englishLabel"`
	LocaleSubkey string `json:"localeSubkey"`
	Settings     string `json:"settings"`
}

func (e *Engine) lightingTemplates(deviceID int) []lightingTemplate {
	rows, err := e.db.Query(`SELECT t.id, t.englishLabel, t.localeSubkey, t.settings FROM lighting_templates t
		JOIN lighting_templates_per_device p ON p.template_id = t.id WHERE p.device_id = ?`, deviceID)
	if err != nil {
		return []lightingTemplate{}
	}
	defer rows.Close()
	out := []lightingTemplate{}
	for rows.Next() {
		var t lightingTemplate
		if rows.Scan(&t.ID, &t.EnglishLabel, &t.LocaleSubkey, &t.Settings) == nil {
			out = append(out, t)
		}
	}
	return out
}

// ensureUser makes sure the single local user exists; the Engine scopes
// configurations and device visibility per user.
func (e *Engine) ensureUser() error {
	var id int
	err := e.db.QueryRow(`SELECT id FROM users WHERE current = 1 LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		err = e.db.QueryRow(`SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&id)
		if err == sql.ErrNoRows {
			res, err := e.db.Exec(`INSERT INTO users (name, username, current) VALUES ('Local User', 'local', 1)`)
			if err != nil {
				return fmt.Errorf("create user: %w", err)
			}
			n, _ := res.LastInsertId()
			id = int(n)
		} else if err == nil {
			e.db.Exec(`UPDATE users SET current = 1 WHERE id = ?`, id)
		}
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	e.userID = id
	return nil
}

// userDevice tracks first-plugin state and hidden/optimized flags.
type userDevice struct {
	Hidden       bool
	OptimizedFor int
	FirstPlugin  bool
	FirstConnect string
}

func (e *Engine) userDevice(deviceID int, create bool) userDevice {
	var u userDevice
	var hidden, first int
	err := e.db.QueryRow(`SELECT hidden, optimized_for, first_plugin, first_connection_time FROM users_devices WHERE user_id = ? AND device_id = ?`,
		e.userID, deviceID).Scan(&hidden, &u.OptimizedFor, &first, &u.FirstConnect)
	if err == sql.ErrNoRows && create {
		e.db.Exec(`INSERT INTO users_devices (user_id, device_id) VALUES (?, ?)`, e.userID, deviceID)
		return e.userDevice(deviceID, false)
	}
	u.Hidden, u.FirstPlugin = hidden != 0, first != 0
	return u
}

func jsonOrEmpty(s string) json.RawMessage {
	if s == "" || !json.Valid([]byte(s)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}
