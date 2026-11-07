package devicert

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/steelseries/golisp"
)

// Forms such as (field ...) or (unaligned) evaluate to markers that the
// enclosing (struct ...) or (api ...) collects.
type marker struct {
	kind string
	val  any
}

const markerType = "gglinux.marker"

func mk(kind string, val any) *golisp.Data {
	m := &marker{kind, val}
	return golisp.ObjectWithTypeAndValue(markerType, unsafe.Pointer(m))
}

func asMarker(d *golisp.Data) *marker {
	if golisp.ObjectP(d) && golisp.ObjectType(d) == markerType {
		return (*marker)(golisp.ObjectValue(d))
	}
	return nil
}

func sym(d *golisp.Data) string {
	if golisp.SymbolP(d) || golisp.StringP(d) {
		return golisp.StringValue(d)
	}
	return ""
}

func list(args *golisp.Data) []*golisp.Data { return golisp.ToArray(args) }

func evalInt(args []*golisp.Data, i int, env *golisp.SymbolTableFrame) (int64, error) {
	if i >= len(args) {
		return 0, fmt.Errorf("missing argument %d", i+1)
	}
	v, err := instance.eval(args[i], env)
	if err != nil {
		return 0, err
	}
	if golisp.FloatP(v) {
		return int64(golisp.FloatValue(v)), nil
	}
	return golisp.IntegerValue(v), nil
}

func currentDecl(env *golisp.SymbolTableFrame) (*Decl, error) {
	d := declFor(env)
	if d == nil {
		return nil, fmt.Errorf("not inside a (device ...) declaration")
	}
	return d, nil
}

func sf(name, arity string, fn func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error)) {
	golisp.MakeSpecialForm(name, arity, func(args *golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return fn(list(args), env)
	})
}

func pf(name, arity string, fn func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error)) {
	golisp.MakePrimitiveFunction(name, arity, func(args *golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return fn(list(args), env)
	})
}

func registerDSL() {
	sf("device", "*", defDevice)
	sf("includable", "*", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		// Indexed ahead of time; when evaluated directly, remember it
		// under the file being loaded.
		if instance.curFile != "" {
			instance.includables[strings.TrimSuffix(filepath.Base(instance.curFile), ".lsp")] = golisp.ArrayToList(a)
		}
		return nil, nil
	})
	pf("include", "1", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		name := golisp.StringValue(a[0])
		body, ok := instance.includables[name]
		if !ok {
			body, ok = instance.deviceBodies[name]
		}
		if !ok {
			return nil, fmt.Errorf("include: no includable named %q", name)
		}
		for c := body; golisp.NotNilP(c); c = golisp.Cdr(c) {
			if _, err := instance.eval(golisp.Car(c), env); err != nil {
				slog.Debug("include form error", "include", name, "err", err)
				instance.loadError("include "+name+" "+truncate(golisp.String(golisp.Car(c)), 60), err)
			}
		}
		return nil, nil
	})
	sf("init", "*", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := currentDecl(env)
		if err != nil {
			return nil, err
		}
		d.Init = golisp.ArrayToList(a)
		return nil, nil
	})
	sf("shutdown", "*", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := currentDecl(env)
		if err != nil {
			return nil, err
		}
		d.Shutdown = golisp.ArrayToList(a)
		return nil, nil
	})

	declInt := func(name string, set func(d *Decl, v int64)) {
		pf(name, "1", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
			d, err := currentDecl(env)
			if err != nil {
				return nil, err
			}
			set(d, golisp.IntegerValue(a[0]))
			return nil, nil
		})
	}
	declInt("usage-page", func(d *Decl, v int64) { d.UsagePage = uint16(v) })
	declInt("usage", func(d *Decl, v int64) { d.Usage = uint16(v) })
	declInt("interface-num", func(d *Decl, v int64) { d.InterfaceNum = int(v) })
	declInt("sequence-step-length-ms", func(d *Decl, v int64) { d.SequenceStepMs = int(v) })
	declInt("start-delay", func(d *Decl, v int64) { d.StartDelayMs = int(v) })
	pf("sync-interface", "3", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := currentDecl(env)
		if err != nil {
			return nil, err
		}
		d.HasSync = true
		d.SyncUsagePage = uint16(golisp.IntegerValue(a[0]))
		d.SyncUsage = uint16(golisp.IntegerValue(a[1]))
		d.SyncNum = int(golisp.IntegerValue(a[2]))
		return nil, nil
	})
	declFlag := func(name string, set func(d *Decl)) {
		pf(name, "0", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
			if d := declFor(env); d != nil {
				set(d)
			}
			return nil, nil
		})
	}
	declFlag("support-directwrite", func(d *Decl) { d.SupportsDirectWrite = true })
	declFlag("support-single-directwrite", func(d *Decl) { d.SupportsSingleDirectWrite = true })
	declFlag("support-keyblink", func(d *Decl) { d.SupportsKeyBlink = true })
	declFlag("support-playvibration", func(d *Decl) { d.SupportsPlayVibration = true })
	declFlag("support-onboard-profiles", func(d *Decl) { d.SupportsOnboardProfiles = true })
	declFlag("pause-prismsync-and-gamesense-during-deploy", func(d *Decl) { d.PausePrismSyncOnDeploy = true })
	declFlag("prism-sync-v2-whitelisted", func(d *Decl) { d.PrismSyncV2Whitelisted = true })
	declFlag("always-write-all-settings", func(d *Decl) { d.AlwaysWriteAllSettings = true })
	declFlag("allow-disabling-deployment", func(d *Decl) { d.AllowDisablingDeployment = true })
	declFlag("not-bootloader", func(d *Decl) { d.NotBootloader = true })
	declFlag("hide-in-taskbar-context-menu", func(d *Decl) { d.HideInTaskbarContextMenu = true })
	sf("device-card-app", "*", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		if d := declFor(env); d != nil && len(a) > 0 {
			if v, err := instance.eval(a[0], env); err == nil {
				d.DeviceCardApp = sym(v)
			}
		}
		return nil, nil
	})
	pf("background-software", "2", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		if d := declFor(env); d != nil {
			d.BackgroundSoftwareID, d.BackgroundSoftwareVersion = sym(a[0]), sym(a[1])
		}
		return nil, nil
	})
	pf("command-interface-event-function", "2", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		if d := declFor(env); d != nil {
			d.CommandInterfaceEventType = golisp.IntegerValue(a[0])
			d.CommandInterfaceEventFunc = a[1]
		}
		return nil, nil
	})

	registerStructForms()
	registerApiForms()
	registerSequenceForms()
}

func defDevice(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
	if len(a) == 0 {
		return nil, fmt.Errorf("device: missing id")
	}
	idv, err := instance.eval(a[0], env)
	if err != nil {
		return nil, err
	}
	id := uint32(golisp.IntegerValue(idv))
	name := sym(a[0])
	d := &Decl{
		rt: instance, ID: id, Name: name, InterfaceNum: -1,
		Structs: map[string]*Struct{}, Apis: map[string]*Api{},
		sequences: map[string]*Sequence{}, SequenceStepMs: 33,
	}
	d.Env = golisp.NewSymbolTableFrameBelow(golisp.Global, fmt.Sprintf("device %#x", id))
	d.Env.BindLocallyTo(golisp.Intern(declSymbol), golisp.ObjectWithTypeAndValue(declObjType, unsafe.Pointer(d)))
	for _, form := range a[1:] {
		if _, err := instance.eval(form, d.Env); err != nil {
			slog.Debug("device form error", "device", fmt.Sprintf("%#x", id), "form", truncate(golisp.String(form), 80), "err", err)
			instance.loadError(fmt.Sprintf("%s %s", name, truncate(golisp.String(form), 60)), err)
		}
	}
	instance.mu.Lock()
	instance.decls[id] = d
	instance.mu.Unlock()
	return nil, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// ---------------------------------------------------------------- structs

func registerStructForms() {
	pf("unaligned", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return mk("unaligned", nil), nil })
	pf("aligned", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return mk("aligned", nil), nil })
	pf("constant", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return mk("constant", golisp.IntegerValue(a[0])), nil
	})
	pf("range", "2", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return mk("range", [2]int64{golisp.IntegerValue(a[0]), golisp.IntegerValue(a[1])}), nil
	})
	pf("excluding", "2", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return mk("excluding", [2]int64{golisp.IntegerValue(a[0]), golisp.IntegerValue(a[1])}), nil
	})
	pf("values", ">=1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		var vs []int64
		for _, x := range a {
			if golisp.ListP(x) {
				for _, e := range golisp.ToArray(x) {
					vs = append(vs, golisp.IntegerValue(e))
				}
			} else {
				vs = append(vs, golisp.IntegerValue(x))
			}
		}
		return mk("values", vs), nil
	})
	pf("size", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return mk("size", golisp.IntegerValue(a[0])), nil
	})
	pf("repeat", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return mk("repeat", golisp.IntegerValue(a[0])), nil
	})
	sf("deferred-validation", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return mk("deferred", a[0]), nil
	})
	sf("to-json", "1", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		fn, err := instance.eval(a[0], env)
		return mk("to-json", fn), err
	})
	sf("from-json", "1", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		fn, err := instance.eval(a[0], env)
		return mk("from-json", fn), err
	})
	sf("field", "*", defField)
	sec := func(kind string) func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
			var items []*marker
			for _, f := range a {
				v, err := instance.eval(f, env)
				if err != nil {
					return nil, err
				}
				items = append(items, collectMarkers(v)...)
			}
			return mk(kind, items), nil
		}
	}
	sf("outgoing", "*", sec("outgoing"))
	sf("incoming", "*", sec("incoming"))
	sf("common", "*", sec("common"))
	sf("fields-from-struct", "1", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := currentDecl(env)
		if err != nil {
			return nil, err
		}
		st := d.Struct(sym(a[0]))
		if st == nil {
			return nil, fmt.Errorf("fields-from-struct: unknown struct %s", sym(a[0]))
		}
		var items []*marker
		for _, f := range st.Outgoing.Fields {
			items = append(items, &marker{"field", f})
		}
		return mk("fieldlist", items), nil
	})
	sf("constant-replacement", "2", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		v, err := instance.eval(a[1], env)
		if err != nil {
			return nil, err
		}
		return mk("constrepl", struct {
			field string
			val   int64
		}{sym(a[0]), golisp.IntegerValue(v)}), nil
	})
	sf("struct", "*", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := currentDecl(env)
		if err != nil {
			return nil, err
		}
		st, err := buildStruct(d, sym(a[0]), a[1:], env, nil)
		if err != nil {
			return nil, err
		}
		d.mu.Lock()
		d.Structs[st.Name] = st
		d.mu.Unlock()
		return nil, nil
	})
	sf("self-struct", "*", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return golisp.Eval(golisp.Cons(golisp.Intern("struct"), golisp.ArrayToList(a)), env)
	})
	sf("derived-struct", "*", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := currentDecl(env)
		if err != nil {
			return nil, err
		}
		base := d.Struct(sym(a[1]))
		if base == nil {
			return nil, fmt.Errorf("derived-struct: unknown base %s", sym(a[1]))
		}
		st, err := buildStruct(d, sym(a[0]), a[2:], env, base)
		if err != nil {
			return nil, err
		}
		d.mu.Lock()
		d.Structs[st.Name] = st
		d.mu.Unlock()
		return nil, nil
	})
	sf("struct-outgoing-size", "1", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := currentDecl(env)
		if err != nil {
			return nil, err
		}
		st := d.Struct(sym(a[0]))
		if st == nil {
			return golisp.IntegerWithValue(0), nil
		}
		return golisp.IntegerWithValue(int64(st.Outgoing.size(st.Aligned))), nil
	})
	sf("struct-incoming-size", "1", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := currentDecl(env)
		if err != nil {
			return nil, err
		}
		st := d.Struct(sym(a[0]))
		if st == nil {
			return golisp.IntegerWithValue(0), nil
		}
		return golisp.IntegerWithValue(int64(st.Incoming.size(st.Aligned))), nil
	})
}

func collectMarkers(v *golisp.Data) []*marker {
	if m := asMarker(v); m != nil {
		if m.kind == "fieldlist" {
			return m.val.([]*marker)
		}
		return []*marker{m}
	}
	if golisp.ListP(v) {
		var out []*marker
		for _, e := range golisp.ToArray(v) {
			out = append(out, collectMarkers(e)...)
		}
		return out
	}
	return nil
}

func defField(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
	if len(a) < 2 {
		return nil, fmt.Errorf("field: needs a name and a type")
	}
	f := &Field{Name: sym(a[0]), Type: sym(a[1]), Aligned: true}
	if f.Name == "" {
		v, err := instance.eval(a[0], env)
		if err != nil {
			return nil, err
		}
		f.Name = sym(v)
	}
	for _, o := range a[2:] {
		v, err := instance.eval(o, env)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.Name, err)
		}
		m := asMarker(v)
		if m == nil {
			continue
		}
		switch m.kind {
		case "constant":
			f.IsConstant, f.Constant = true, uint32(m.val.(int64))
		case "range":
			r := m.val.([2]int64)
			f.Range = &r
		case "excluding":
			r := m.val.([2]int64)
			f.Excluding = &r
		case "values":
			f.Values = m.val.([]int64)
		case "size":
			f.Size = int(m.val.(int64))
		case "repeat":
			f.Repeat = int(m.val.(int64))
		case "deferred":
			f.Deferred = m.val.(*golisp.Data)
		case "to-json":
			f.ToJSON = m.val.(*golisp.Data)
		case "from-json":
			f.FromJSON = m.val.(*golisp.Data)
		case "unaligned":
			f.Aligned = false
		}
	}
	if scalarSize(f.Type) == 0 && f.Type != "bytearray" && f.Type != "varstring" {
		if d := declFor(env); d != nil {
			f.nested = d.Struct(f.Type)
		}
		if f.nested == nil {
			return nil, fmt.Errorf("field %s: unknown type %s", f.Name, f.Type)
		}
	}
	return mk("field", f), nil
}

// buildStruct evaluates struct body forms. base, when set, is copied first
// (derived-struct) and constant-replacements patch its constants.
func buildStruct(d *Decl, name string, body []*golisp.Data, env *golisp.SymbolTableFrame, base *Struct) (*Struct, error) {
	st := &Struct{Name: name, Aligned: true, Outgoing: &Section{}, Incoming: &Section{}}
	var shared, out, in []*Field
	sawSections := false
	if base != nil {
		st.Aligned = base.Aligned
		out = cloneFields(base.Outgoing.Fields)
		in = cloneFields(base.Incoming.Fields)
		sawSections = true
	}
	for _, form := range body {
		v, err := instance.eval(form, env)
		if err != nil {
			return nil, fmt.Errorf("struct %s: %w", name, err)
		}
		for _, m := range collectMarkers(v) {
			switch m.kind {
			case "unaligned":
				st.Aligned = false
			case "aligned":
				st.Aligned = true
			case "field":
				shared = append(shared, m.val.(*Field))
			case "outgoing", "incoming", "common":
				sawSections = true
				var fs []*Field
				for _, fm := range m.val.([]*marker) {
					if fm.kind == "field" {
						fs = append(fs, fm.val.(*Field))
					}
				}
				switch m.kind {
				case "outgoing":
					out = append(out, fs...)
				case "incoming":
					in = append(in, fs...)
				default:
					out = append(out, fs...)
					in = append(in, fs...)
				}
			case "constrepl":
				cr := m.val.(struct {
					field string
					val   int64
				})
				for _, fs := range [][]*Field{out, in, shared} {
					for _, f := range fs {
						if f.Name == cr.field {
							f.IsConstant, f.Constant, f.ConstReplaced = true, uint32(cr.val), true
						}
					}
				}
			}
		}
	}
	if !sawSections {
		out, in = shared, shared
	} else {
		out = append(out, shared...)
		in = append(in, shared...)
	}
	st.Outgoing.Fields, st.Incoming.Fields = out, in
	return st, nil
}

func cloneFields(fs []*Field) []*Field {
	out := make([]*Field, len(fs))
	for i, f := range fs {
		c := *f
		out[i] = &c
	}
	return out
}
