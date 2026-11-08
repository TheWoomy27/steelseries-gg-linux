package engine

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	"ggl/internal/httpx"
)

// Parametric EQ presets (device_game_eq_presets). Game presets (preset_type
// 0) ship with the Engine database; device_id 0 means every headset. Custom
// presets (preset_type 1) belong to one device, and the config window's edits
// are staged in unsaved_eq_data until it saves or reverts them.

const (
	gameEQPreset   = 0
	customEQPreset = 1
)

type eqFilter struct {
	Enabled    bool    `json:"enabled"`
	Frequency  float64 `json:"frequency"`
	Gain       float64 `json:"gain"`
	QFactor    float64 `json:"qFactor"`
	FilterType string  `json:"type"`
}

type customBand struct {
	Enabled    int     `json:"enabled"`
	FilterType int     `json:"filter_type"`
	Frequency  float64 `json:"frequency"`
	Gain       float64 `json:"gain"`
	QFactor    float64 `json:"q_factor"`
}

// Ten bands keyed filter1..filter10 (game presets) or eq_band_1..eq_band_10
// (custom presets). The client reads them with Object.values, so they are
// written in band order rather than a map's sorted order.
type (
	gameEQData   [10]eqFilter
	customEQData [10]customBand
)

func (d gameEQData) MarshalJSON() ([]byte, error)    { return marshalBands("filter", d[:]) }
func (d *gameEQData) UnmarshalJSON(b []byte) error   { return unmarshalBands(b, "filter", d[:]) }
func (d customEQData) MarshalJSON() ([]byte, error)  { return marshalBands("eq_band_", d[:]) }
func (d *customEQData) UnmarshalJSON(b []byte) error { return unmarshalBands(b, "eq_band_", d[:]) }

func marshalBands[T any](prefix string, bands []T) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, band := range bands {
		v, err := json.Marshal(band)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(strconv.Quote(prefix + strconv.Itoa(i+1)))
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func unmarshalBands[T any](b []byte, prefix string, bands []T) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for i := range bands {
		if v, ok := m[prefix+strconv.Itoa(i+1)]; ok {
			if err := json.Unmarshal(v, &bands[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

type gameEQPresetJSON struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"displayName"`
	AliasName   string     `json:"aliasName"`
	EqData      gameEQData `json:"eqData"`
	DeviceID    int        `json:"deviceId"`
	PresetType  int        `json:"preset_type"`
	Metadata    struct {
		VirtualAudioDevice string `json:"virtualAudioDevice,omitzero"`
		ShortName          string `json:"shortName,omitzero"`
		SchemaVersion      int    `json:"schemaVersion,omitzero"`
	} `json:"metadata"`
	SupportedMode int `json:"supportedMode"`
}

type customEQPresetJSON struct {
	ID            string       `json:"id"`
	DisplayName   string       `json:"displayName"`
	AliasName     string       `json:"aliasName"`
	EqData        customEQData `json:"eqData"`
	DeviceID      int          `json:"device_id"`
	PresetType    int          `json:"preset_type"`
	SupportedMode int          `json:"supportedMode"`
}

func (e *Engine) gameEQPresets() ([]gameEQPresetJSON, error) {
	rows, err := e.db.Query(`SELECT id, display_name, alias_name, eq_preset_data, device_id, preset_type, metadata, supported_mode
		FROM device_game_eq_presets WHERE preset_type = ?`, gameEQPreset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	presets := []gameEQPresetJSON{}
	for rows.Next() {
		var p gameEQPresetJSON
		var data sql.NullString
		var meta string
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.AliasName, &data, &p.DeviceID, &p.PresetType, &meta, &p.SupportedMode); err != nil {
			return nil, err
		}
		if data.Valid {
			json.Unmarshal([]byte(data.String), &p.EqData)
		}
		if meta != "" {
			json.Unmarshal([]byte(meta), &p.Metadata)
		}
		presets = append(presets, p)
	}
	return presets, rows.Err()
}

// customEQPresets lists a device's custom presets with any unsaved edits
// applied.
func (e *Engine) customEQPresets(deviceID int) ([]customEQPresetJSON, error) {
	rows, err := e.db.Query(`SELECT id, display_name, alias_name, COALESCE(unsaved_eq_data, eq_preset_data), device_id, preset_type, supported_mode
		FROM device_game_eq_presets WHERE preset_type = ? AND device_id = ?`, customEQPreset, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	presets := []customEQPresetJSON{}
	for rows.Next() {
		var p customEQPresetJSON
		var data sql.NullString
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.AliasName, &data, &p.DeviceID, &p.PresetType, &p.SupportedMode); err != nil {
			return nil, err
		}
		if data.Valid {
			json.Unmarshal([]byte(data.String), &p.EqData)
		}
		presets = append(presets, p)
	}
	return presets, rows.Err()
}

// eqPresetsChanged tells config windows showing deviceID to reload its
// presets.
func (e *Engine) eqPresetsChanged(deviceID int) {
	e.emit("EVENT_EQ_PRESETS_CHANGE", map[string]any{"deviceId": deviceID})
}

func (e *Engine) eqPresetRoutes() {
	s := e.srv
	gamePresets := func(w http.ResponseWriter, r *http.Request) {
		presets, err := e.gameEQPresets()
		if err != nil {
			httpx.Error(w, 500, err.Error())
			return
		}
		httpx.JSON(w, map[string]any{"gameEqPresets": presets})
	}
	// The client POSTs this one although the Engine registers it as GET.
	s.Handle("GET /parametricEQ/deviceGameEQPreset", gamePresets)
	s.Handle("POST /parametricEQ/deviceGameEQPreset", gamePresets)

	s.Handle("POST /parametricEQ/{id}/deviceCustomEQPreset", func(w http.ResponseWriter, r *http.Request) {
		presets, err := e.customEQPresets(pathInt(r, "id"))
		if err != nil {
			httpx.Error(w, 500, err.Error())
			return
		}
		httpx.JSON(w, map[string]any{"customEqPresetKey": presets})
	})

	// Creates a custom preset, or stages new bands for the device's preset
	// of that name and mode.
	s.Handle("POST /parametricEQ/addOrEditCustomEQPreset", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			DeviceID      int          `json:"device_id"`
			DisplayName   string       `json:"displayName"`
			SupportedMode int          `json:"supportedMode"`
			EqData        customEQData `json:"eqData"`
			AliasName     string       `json:"aliasName"`
		}
		if err := httpx.Decode(r, &p); err != nil {
			httpx.Error(w, 400, err.Error())
			return
		}
		data, _ := json.Marshal(p.EqData)
		var id string
		err := e.db.QueryRow(`SELECT id FROM device_game_eq_presets WHERE lower(display_name) = lower(?) AND preset_type = ?
			AND (device_id = ? OR device_id = 0) AND (supported_mode & ?) = ?`,
			p.DisplayName, customEQPreset, p.DeviceID, p.SupportedMode, p.SupportedMode).Scan(&id)
		switch {
		case err == sql.ErrNoRows:
			_, err = e.db.Exec(`INSERT INTO device_game_eq_presets (id, display_name, alias_name, eq_preset_data, device_id, preset_type, supported_mode)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, newUUID(), p.DisplayName, p.AliasName, string(data), p.DeviceID, customEQPreset, p.SupportedMode)
		case err == nil:
			_, err = e.db.Exec(`UPDATE device_game_eq_presets SET display_name = ?, unsaved_eq_data = ?, alias_name = ? WHERE id = ?`,
				p.DisplayName, string(data), p.AliasName, id)
		}
		if err != nil {
			httpx.Error(w, 500, err.Error())
			return
		}
		e.eqPresetsChanged(p.DeviceID)
		httpx.OK(w)
	})

	s.Handle("POST /parametricEQ/{pid}/removeCustomEQPreset", func(w http.ResponseWriter, r *http.Request) {
		var deviceID int
		err := e.db.QueryRow(`SELECT device_id FROM device_game_eq_presets WHERE id = ? AND preset_type = ?`,
			r.PathValue("pid"), customEQPreset).Scan(&deviceID)
		if err != nil {
			httpx.Error(w, 404, "no such custom EQ preset")
			return
		}
		e.db.Exec(`DELETE FROM device_game_eq_presets WHERE id = ?`, r.PathValue("pid"))
		e.eqPresetsChanged(deviceID)
		httpx.OK(w)
	})

	s.Handle("POST /parametricEQ/renameCustomEQPreset", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
			AliasName   string `json:"aliasName"`
		}
		if err := httpx.Decode(r, &p); err != nil {
			httpx.Error(w, 400, err.Error())
			return
		}
		var deviceID int
		if err := e.db.QueryRow(`SELECT device_id FROM device_game_eq_presets WHERE id = ?`, p.ID).Scan(&deviceID); err != nil {
			httpx.Error(w, 404, "no such EQ preset")
			return
		}
		e.db.Exec(`UPDATE device_game_eq_presets SET display_name = ?, alias_name = ? WHERE id = ?`, p.DisplayName, p.AliasName, p.ID)
		e.eqPresetsChanged(deviceID)
		httpx.OK(w)
	})

	s.Handle("POST /parametricEQ/{id}/saveAllCustomPEQ", func(w http.ResponseWriter, r *http.Request) {
		id := pathInt(r, "id")
		e.db.Exec(`UPDATE device_game_eq_presets SET eq_preset_data = unsaved_eq_data WHERE device_id = ? AND unsaved_eq_data NOT NULL`, id)
		e.db.Exec(`UPDATE device_game_eq_presets SET unsaved_eq_data = NULL WHERE device_id = ? AND unsaved_eq_data NOT NULL`, id)
		e.eqPresetsChanged(id)
		httpx.OK(w)
	})

	s.Handle("POST /parametricEQ/{id}/revertCustomPEQ", func(w http.ResponseWriter, r *http.Request) {
		id := pathInt(r, "id")
		e.db.Exec(`UPDATE device_game_eq_presets SET unsaved_eq_data = NULL WHERE device_id = ? AND unsaved_eq_data NOT NULL`, id)
		e.eqPresetsChanged(id)
		httpx.OK(w)
	})
}
