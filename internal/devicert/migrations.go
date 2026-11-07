package devicert

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"

	"github.com/steelseries/golisp"
)

// Configuration migrations (configurationMigrations/*.migration) upgrade
// stored configuration settings when a device's settings format changes:
//
//	(configuration-migration apex_7
//	  (migration-schema 1 (generate-guid settings))
//	  (migration-schema 6 (merge-mappings settings) ...))
//
// Each schema body runs with settings and undeployedSettings bound to the
// configuration's JSON as golisp frames. Settings record the schema they
// were migrated to in their "schema" key, so a body runs at most once.

type migrationSchema struct {
	version int64
	body    *golisp.Data
	env     *golisp.SymbolTableFrame // the file's environment
}

// LoadMigrations reads every migration file in dir.
func (rt *Runtime) LoadMigrations(dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.migration"))
	if err != nil {
		return err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.migrations = map[string][]migrationSchema{}
	for _, f := range files {
		if err := rt.loadMigrationFile(f); err != nil {
			slog.Warn("configuration migration", "file", filepath.Base(f), "err", err)
			rt.loadError(filepath.Base(f), err)
		}
	}
	for name, schemas := range rt.migrations {
		sort.SliceStable(schemas, func(i, j int) bool { return schemas[i].version < schemas[j].version })
		rt.migrations[name] = schemas
	}
	return nil
}

func (rt *Runtime) loadMigrationFile(path string) error {
	src, err := readSpec(path)
	if err != nil {
		return err
	}
	forms, err := golisp.ParseAll(src)
	if err != nil {
		return err
	}
	env := golisp.NewSymbolTableFrameBelow(golisp.Global, filepath.Base(path))
	for _, form := range forms {
		if !golisp.PairP(form) || !golisp.SymbolP(golisp.Car(form)) || golisp.StringValue(golisp.Car(form)) != "configuration-migration" {
			if _, err := rt.eval(form, env); err != nil {
				return err
			}
			continue
		}
		var names []string
		var schemas []migrationSchema
		for c := golisp.Cdr(form); golisp.NotNilP(c); c = golisp.Cdr(c) {
			arg := golisp.Car(c)
			if golisp.SymbolP(arg) || golisp.StringP(arg) {
				names = append(names, golisp.StringValue(arg))
				continue
			}
			if golisp.PairP(arg) && golisp.StringValue(golisp.Car(arg)) == "quote" {
				// '(apex-350 hp_omen_apex-350)
				for n := golisp.Cadr(arg); golisp.NotNilP(n); n = golisp.Cdr(n) {
					names = append(names, golisp.StringValue(golisp.Car(n)))
				}
				continue
			}
			if !golisp.PairP(arg) || golisp.StringValue(golisp.Car(arg)) != "migration-schema" {
				return fmt.Errorf("configuration-migration %v: expected migration-schema, got %s", names, golisp.String(arg))
			}
			v, err := rt.eval(golisp.Cadr(arg), env)
			if err != nil || !golisp.IntegerP(v) {
				return fmt.Errorf("configuration-migration %v: schema version should be a number, got %s", names, golisp.String(golisp.Cadr(arg)))
			}
			if golisp.IntegerValue(v) == 0 {
				return fmt.Errorf("configuration-migration %v: schema version cannot be 0", names)
			}
			schemas = append(schemas, migrationSchema{golisp.IntegerValue(v), golisp.Cddr(arg), env})
		}
		for _, n := range names {
			rt.migrations[n] = mergeSchemas(rt.migrations[n], schemas)
		}
	}
	return nil
}

// mergeSchemas adds schemas to a device's list; a later definition of the
// same version replaces the earlier one.
func mergeSchemas(have, add []migrationSchema) []migrationSchema {
	for _, s := range add {
		replaced := false
		for i := range have {
			if have[i].version == s.version {
				have[i], replaced = s, true
			}
		}
		if !replaced {
			have = append(have, s)
		}
	}
	return have
}

// MigrateSettings brings a configuration's settings up to the newest schema
// its device's migrations define. changed is false when nothing ran.
func (rt *Runtime) MigrateSettings(deviceName, settings, undeployed string) (newSettings, newUndeployed string, changed bool, err error) {
	rt.mu.Lock()
	schemas := rt.migrations[deviceName]
	rt.mu.Unlock()
	if len(schemas) == 0 {
		return settings, undeployed, false, nil
	}
	if undeployed == "" {
		undeployed = "{}"
	}
	s := golisp.JsonStringToLispWithFrames(settings)
	u := golisp.JsonStringToLispWithFrames(undeployed)
	if !golisp.FrameP(s) || !golisp.FrameP(u) {
		return settings, undeployed, false, nil
	}
	current := int64(0)
	if v := golisp.FrameValue(s).Get("schema:"); golisp.IntegerP(v) {
		current = golisp.IntegerValue(v)
	}
	target := schemas[len(schemas)-1].version
	if current >= target {
		return settings, undeployed, false, nil
	}
	for _, m := range schemas {
		if m.version <= current {
			continue
		}
		env := golisp.NewSymbolTableFrameBelow(m.env, fmt.Sprintf("schema_%d", m.version))
		env.BindLocallyTo(golisp.Intern("settings"), s)
		env.BindLocallyTo(golisp.Intern("undeployedSettings"), u)
		if _, err := rt.evalBody(m.body, env); err != nil {
			return settings, undeployed, false, fmt.Errorf("%s schema %d: %w", deviceName, m.version, err)
		}
	}
	golisp.FrameValue(s).Set("schema:", golisp.IntegerWithValue(target))
	return golisp.LispWithFramesToJsonString(s), golisp.LispWithFramesToJsonString(u), true, nil
}
