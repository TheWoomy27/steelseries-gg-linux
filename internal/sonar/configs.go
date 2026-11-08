package sonar

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// A config is one channel's processing settings (Sonar's "configs": the
// user's own and the shipped presets). Each channel has one selected config.
type config struct {
	ID                    string
	Name                  string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	VAD                   int
	Data                  any
	SchemaVersion         int
	IsSelected            bool
	IsPreset              bool
	DefaultData           any
	Image                 string
	DefaultDataLastUpdate time.Time
	IsFavorite            bool
	FavoritePosition      int
	GameIDs               string
	ReleaseVersion        *string
}

// Default presets each channel falls back to.
var defaultPresetIDs = map[int]string{
	vadGame:        "bff7e90b-03db-49ea-bf29-10a909825c0d",
	vadChatRender:  "cf261e8e-3e83-4da0-bdd6-2391031bfb05",
	vadChatCapture: "fb0f9987-dacf-410e-8752-f5ac8ad91353",
	vadMedia:       "bef7d9a1-5a46-4895-ac78-f05d36f5baf2",
	vadAux:         "c7fcef93-d1f3-447d-b61b-6c9051d60ac4",
}

const maxFavorites = 9

// argError is Sonar's ArgumentException: the API answers 400 for it and
// 500 for anything else.
type argError struct{ msg string }

func (e argError) Error() string { return e.msg }

func argErrorf(format string, a ...any) error { return argError{fmt.Sprintf(format, a...)} }

// Newtonsoft writes DateTime values read from the database without a zone;
// EF Core stores them as text.
const (
	jsonTime = "2006-01-02T15:04:05.9999999"
	dbTime   = "2006-01-02 15:04:05.9999999"
)

func parseDBTime(s string) time.Time {
	for _, layout := range []string{dbTime, jsonTime} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (c *config) MarshalJSON() ([]byte, error) {
	o := newObject()
	o.set("id", c.ID)
	o.set("name", c.Name)
	o.set("createdAt", c.CreatedAt.Format(jsonTime))
	o.set("updatedAt", c.UpdatedAt.Format(jsonTime))
	o.set("virtualAudioDevice", camel(vadNames[c.VAD]))
	o.set("data", c.Data)
	o.set("schemaVersion", c.SchemaVersion)
	o.set("isPreset", c.IsPreset)
	o.set("defaultData", c.DefaultData)
	o.set("image", c.Image)
	o.set("isFavorite", c.IsFavorite)
	o.set("favoritePosition", c.FavoritePosition)
	o.set("releaseVersion", c.ReleaseVersion)
	return o.MarshalJSON()
}

// UnmarshalJSON reads a config the client sends, with ASP.NET's binding
// rules: enums by name or number, missing fields left at their zero value.
func (c *config) UnmarshalJSON(b []byte) error {
	v, err := parseJSON(string(b))
	if err != nil {
		return argError{err.Error()}
	}
	o, ok := v.(*object)
	if !ok {
		return argErrorf("config must be an object")
	}
	str := func(k string) string { s, _ := o.get(k).(string); return s }
	c.ID = strings.ToLower(str("id"))
	c.Name = strings.TrimSpace(str("name"))
	switch x := o.get("virtualAudioDevice").(type) {
	case string:
		vad, ok := vadByName(x)
		if !ok {
			vad, _ = strconv.Atoi(x)
		}
		c.VAD = vad
	case json.Number:
		n, _ := x.Int64()
		c.VAD = int(n)
	}
	c.Data = o.get("data")
	c.DefaultData = o.get("defaultData")
	if n, ok := o.get("schemaVersion").(json.Number); ok {
		v, _ := n.Int64()
		c.SchemaVersion = int(v)
	}
	c.IsPreset, _ = o.get("isPreset").(bool)
	c.IsFavorite, _ = o.get("isFavorite").(bool)
	c.Image = str("image")
	if s, ok := o.get("releaseVersion").(string); ok {
		c.ReleaseVersion = &s
	}
	if _, ok := vadNames[c.VAD]; !ok {
		return argErrorf("unknown virtualAudioDevice")
	}
	return nil
}

const configCols = `id, name, vad, data, schema_version, created_at, updated_at, is_preset, default_data,
	default_data_last_update, image, is_favorite, favorite_position, game_ids, release_version,
	EXISTS (SELECT 1 FROM selected_config s WHERE s.config_id = configs.id)`

func scanConfig(row interface{ Scan(...any) error }) (*config, error) {
	var c config
	var data, defData, created, updated string
	var defUpdate, release sql.NullString
	if err := row.Scan(&c.ID, &c.Name, &c.VAD, &data, &c.SchemaVersion, &created, &updated, &c.IsPreset, &defData,
		&defUpdate, &c.Image, &c.IsFavorite, &c.FavoritePosition, &c.GameIDs, &release, &c.IsSelected); err != nil {
		return nil, err
	}
	c.ID = strings.ToLower(c.ID)
	c.CreatedAt, c.UpdatedAt = parseDBTime(created), parseDBTime(updated)
	c.DefaultDataLastUpdate = parseDBTime(defUpdate.String)
	if release.Valid {
		c.ReleaseVersion = &release.String
	}
	var err error
	if c.Data, err = parseJSON(data); err != nil {
		slog.Warn("sonar: unreadable config data", "config", c.ID, "err", err)
	}
	if c.DefaultData, err = parseJSON(defData); err != nil {
		slog.Warn("sonar: unreadable config default data", "config", c.ID, "err", err)
	}
	return &c, nil
}

func (s *Service) queryConfigs(where string, args ...any) ([]*config, error) {
	rows, err := s.db.Query(`SELECT `+configCols+` FROM configs `+where+` ORDER BY rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*config{}
	for rows.Next() {
		c, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) configByID(id string) (*config, error) {
	cfgs, err := s.queryConfigs(`WHERE lower(id) = ?`, strings.ToLower(id))
	if err != nil {
		return nil, err
	}
	if len(cfgs) == 0 {
		return nil, fmt.Errorf("config %s not found", id)
	}
	return cfgs[0], nil
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (s *Service) writeConfig(c *config, insert bool) error {
	now := time.Now().UTC()
	c.UpdatedAt = now
	if insert {
		c.CreatedAt = now
	}
	args := []any{c.Name, c.VAD, jsonText(c.Data), c.SchemaVersion, c.UpdatedAt.Format(dbTime), c.IsPreset,
		jsonText(c.DefaultData), c.DefaultDataLastUpdate.Format(dbTime), c.Image, c.IsFavorite, c.FavoritePosition,
		c.GameIDs, c.ReleaseVersion}
	if insert {
		_, err := s.db.Exec(`INSERT INTO configs (name, vad, data, schema_version, updated_at, is_preset, default_data,
			default_data_last_update, image, is_favorite, favorite_position, game_ids, release_version, created_at, id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, append(args, c.CreatedAt.Format(dbTime), c.ID)...)
		return err
	}
	_, err := s.db.Exec(`UPDATE configs SET name = ?, vad = ?, data = ?, schema_version = ?, updated_at = ?, is_preset = ?,
		default_data = ?, default_data_last_update = ?, image = ?, is_favorite = ?, favorite_position = ?, game_ids = ?,
		release_version = ? WHERE lower(id) = ?`, append(args, c.ID)...)
	return err
}

func newGUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// upgradeConfigs brings stored configs to the latest schemas at startup.
func (s *Service) upgradeConfigs() {
	cfgs, err := s.queryConfigs(``)
	if err != nil {
		slog.Error("sonar: reading configs", "err", err)
		return
	}
	n := 0
	for _, c := range cfgs {
		if c.SchemaVersion >= latestSchema[c.VAD] {
			continue
		}
		if err := upgrade(c); err != nil {
			slog.Warn("sonar: config upgrade failed", "config", c.ID, "name", c.Name, "err", err)
			continue
		}
		if err := s.writeConfig(c, false); err != nil {
			slog.Warn("sonar: saving upgraded config", "config", c.ID, "err", err)
			continue
		}
		n++
	}
	if n > 0 {
		slog.Info("sonar: upgraded configs", "count", n)
	}
}

func (s *Service) configs(vad int) ([]*config, error) {
	where, args := ``, []any{}
	if vad != 0 {
		where, args = `WHERE vad = ?`, []any{vad}
	}
	all, err := s.queryConfigs(where, args...)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, c := range all {
		if c.SchemaVersion == latestSchema[c.VAD] {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Service) selectedConfigs() ([]*config, error) {
	return s.queryConfigs(`WHERE id IN (SELECT config_id FROM selected_config)`)
}

// checkSchema settles a new or edited config's schema version: -1 means the
// latest, and inserts may come from an older schema.
func checkSchema(c *config, insert bool) error {
	latest := latestSchema[c.VAD]
	switch {
	case c.SchemaVersion == -1:
		c.SchemaVersion = latest
	case !insert && c.SchemaVersion != latest:
		return argErrorf("Incorrect SchemaVersion (%d); expecting %d", c.SchemaVersion, latest)
	case c.SchemaVersion <= 0:
		return argErrorf("SchemaVersion (%d) must be greater than 0", c.SchemaVersion)
	case c.SchemaVersion > latest:
		return errSchemaTooBig
	case c.SchemaVersion < latest:
		if !validData(c.Data) {
			c.Data = defaultData(c.VAD)
		}
		return upgrade(c)
	}
	return nil
}

var errSchemaTooBig = argError{"ERROR_SCHEMA_TOO_BIG"}

func (s *Service) insertConfig(c *config) (*config, error) {
	if err := checkSchema(c, true); err != nil {
		return nil, err
	}
	if !validData(c.DefaultData) {
		c.DefaultData = defaultData(c.VAD)
	}
	c.DefaultDataLastUpdate = time.Now().UTC()
	if !validData(c.Data) {
		c.Data = defaultData(c.VAD)
	}
	if c.Image == "" {
		c.Image = defaultImage(c.VAD, c.IsPreset)
	}
	c.ID = newGUID()
	c.IsFavorite, c.FavoritePosition, c.ReleaseVersion = false, -1, nil
	if err := s.writeConfig(c, true); err != nil {
		return nil, err
	}
	return s.configByID(c.ID)
}

func (s *Service) updateConfig(c *config) (*config, error) {
	if err := checkSchema(c, false); err != nil {
		return nil, err
	}
	if !validData(c.Data) {
		c.Data = defaultData(c.VAD)
	}
	if c.Image == "" {
		c.Image = defaultImage(c.VAD, c.IsPreset)
	}
	old, err := s.configByID(c.ID)
	if err != nil {
		return nil, err
	}
	c.IsSelected, c.DefaultDataLastUpdate = old.IsSelected, old.DefaultDataLastUpdate
	c.IsFavorite, c.FavoritePosition, c.GameIDs = old.IsFavorite, old.FavoritePosition, old.GameIDs
	c.CreatedAt = old.CreatedAt
	if !validData(c.DefaultData) {
		c.DefaultData = old.DefaultData
	}
	if err := s.writeConfig(c, false); err != nil {
		return nil, err
	}
	return s.configByID(c.ID)
}

func (s *Service) selectConfig(id string) (*config, error) {
	c, err := s.configByID(id)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM selected_config WHERE vad = ?`, c.VAD); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO selected_config (vad, config_id) VALUES (?, ?)`, c.VAD, c.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	c.IsSelected = true
	return c, nil
}

func (s *Service) deleteConfig(id string) ([]*config, error) {
	c, err := s.configByID(id)
	if err != nil {
		return nil, err
	}
	if c.IsSelected {
		if _, err := s.selectConfig(defaultPresetIDs[c.VAD]); err != nil {
			return nil, err
		}
	}
	if _, err := s.db.Exec(`DELETE FROM configs WHERE lower(id) = ?`, c.ID); err != nil {
		return nil, err
	}
	if c.IsFavorite {
		s.db.Exec(`UPDATE configs SET favorite_position = favorite_position - 1
			WHERE vad = ? AND is_favorite = 1 AND favorite_position > ?`, c.VAD, c.FavoritePosition)
	}
	s.notifyFavorites(c.VAD)
	return s.configs(c.VAD)
}

func (s *Service) favorites(vad int) ([]*config, error) {
	return s.queryConfigs(`WHERE vad = ? AND is_favorite = 1`, vad)
}

func (s *Service) favoriteLimitReached(vad int) bool {
	var n int
	s.db.QueryRow(`SELECT count(*) FROM configs WHERE vad = ? AND is_favorite = 1`, vad).Scan(&n)
	return n >= maxFavorites
}

func (s *Service) setFavorite(id string, favorite bool) ([]*config, error) {
	c, err := s.configByID(id)
	if err != nil {
		return nil, err
	}
	if c.IsFavorite == favorite || favorite && s.favoriteLimitReached(c.VAD) {
		return s.favorites(c.VAD)
	}
	if favorite {
		favs, _ := s.favorites(c.VAD)
		c.IsFavorite, c.FavoritePosition = true, len(favs)
	} else {
		s.db.Exec(`UPDATE configs SET favorite_position = favorite_position - 1
			WHERE vad = ? AND is_favorite = 1 AND favorite_position > ?`, c.VAD, c.FavoritePosition)
		c.IsFavorite, c.FavoritePosition = false, -1
	}
	if err := s.writeConfig(c, false); err != nil {
		return nil, err
	}
	s.notifyFavorites(c.VAD)
	return s.favorites(c.VAD)
}

func (s *Service) setFavoritePosition(id string, position int) ([]*config, error) {
	if position < 0 || position >= maxFavorites {
		return nil, argErrorf("Specified argument was out of the range of valid values. (Parameter 'position')")
	}
	c, err := s.configByID(id)
	if err != nil {
		return nil, err
	}
	if !c.IsFavorite {
		return nil, errors.New("Cannot set favorite position for a non favorite config")
	}
	favs, err := s.queryConfigs(`WHERE vad = ? AND is_favorite = 1 ORDER BY favorite_position`, c.VAD)
	if err != nil {
		return nil, err
	}
	if c.FavoritePosition == position {
		return favs, nil
	}
	position = min(position, len(favs)-1)
	ordered := make([]*config, 0, len(favs))
	for _, f := range favs {
		if f.ID != c.ID {
			ordered = append(ordered, f)
		}
	}
	ordered = append(ordered[:position], append([]*config{c}, ordered[position:]...)...)
	for i, f := range ordered {
		if f.FavoritePosition != i {
			f.FavoritePosition = i
			if err := s.writeConfig(f, false); err != nil {
				return nil, err
			}
		}
	}
	return ordered, nil
}

// notifyFavorites tells Sonar pages a channel's favorites changed. The event
// is serialized with Newtonsoft's defaults, unlike the API's camelCase.
func (s *Service) notifyFavorites(vad int) {
	s.ws.Broadcast("SONAR_EVENT_FAVORITE_CONFIGS_UPDATED", map[string]any{
		"VirtualAudioDevice": vad, "IsFavoriteLimitReached": s.favoriteLimitReached(vad),
	})
}

type configOption struct {
	VirtualAudioDevice     string `json:"virtualAudioDevice"`
	IsFavoriteLimitReached bool   `json:"isFavoriteLimitReached"`
}

func (s *Service) configOptions() []configOption {
	out := []configOption{}
	for _, vad := range []int{vadGame, vadChatRender, vadChatCapture, vadMedia, vadAux} {
		out = append(out, configOption{camel(vadNames[vad]), s.favoriteLimitReached(vad)})
	}
	return out
}
