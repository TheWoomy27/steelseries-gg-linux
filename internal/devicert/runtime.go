// Package devicert runs SteelSeries device specifications.
//
// Each SteelSeries device is described by a GoLisp program (decrypted from
// the installer's *.edevice files by gg-setup). The programs use golisp, the
// interpreter SteelSeries open-sourced, plus a device DSL the Engine adds on
// top: (device ...), (struct ...), (api ...), (chunk ...) and friends. This
// package reimplements that DSL and executes the resulting HID traffic
// through a Transport.
package devicert

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/steelseries/golisp"
)

// Transport performs one HID chunk against a physical device interface.
type Transport interface {
	// Chunk executes a HID chunk (HIDIO, HIDFEATURE, ...) of the given
	// report size on the interface identified by (product id, usage page,
	// usage). It returns the response report, if the chunk type reads one.
	Chunk(target Target, chunkType int, size int, data []byte, timeout time.Duration) ([]byte, error)
	// Connected reports whether a matching interface is present.
	Connected(target Target) bool
}

// Target addresses a HID top-level collection on a device.
type Target struct {
	ProductID uint16
	UsagePage uint16
	Usage     uint16
	Interface int // -1 for any
}

// Hooks lets the engine observe things device code does.
type Hooks interface {
	DeviceEvent(devID uint32, kind string, data map[string]any)
	ActiveConfigSettings(devID uint32) map[string]any
	Log(devID uint32, msg string)
}

type nopHooks struct{}

func (nopHooks) DeviceEvent(uint32, string, map[string]any) {}
func (nopHooks) ActiveConfigSettings(uint32) map[string]any { return nil }
func (nopHooks) Log(uint32, string)                         {}

// Runtime owns the golisp global environment and all device declarations.
// golisp keeps its primitives in a process-wide table, so there is one
// Runtime per process.
type Runtime struct {
	mu           sync.Mutex
	specDir      string
	engineDir    string
	includables  map[string]*golisp.Data
	deviceBodies map[string]*golisp.Data
	deviceFile   map[uint32]string
	loadedFiles  map[string]bool
	decls        map[uint32]*Decl
	transport    Transport
	hooks        Hooks
	globals      map[string]*golisp.Data
	migrations   map[string][]migrationSchema // by device name
	curFile      string
	// AllowFirmware gates APIs marked (is-firmware-update).
	AllowFirmware bool
	// LoadErrors collects spec forms that failed to evaluate.
	LoadErrors []string
}

func (rt *Runtime) loadError(where string, err error) {
	rt.LoadErrors = append(rt.LoadErrors, where+": "+err.Error())
}

var (
	instance *Runtime
	once     sync.Once
)

// New builds the runtime and indexes the specifications in specDir.
func New(specDir, engineDir string, t Transport, hooks Hooks) (*Runtime, error) {
	if hooks == nil {
		hooks = nopHooks{}
	}
	rt := &Runtime{
		specDir:      specDir,
		engineDir:    engineDir,
		includables:  map[string]*golisp.Data{},
		deviceBodies: map[string]*golisp.Data{},
		deviceFile:   map[uint32]string{},
		loadedFiles:  map[string]bool{},
		decls:        map[uint32]*Decl{},
		transport:    t,
		hooks:        hooks,
		globals:      map[string]*golisp.Data{},
	}
	instance = rt
	once.Do(registerBuiltins)
	if err := rt.index(); err != nil {
		return nil, err
	}
	return rt, nil
}

func readSpec(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n"), nil
}

// index scans every spec: (includable ...) bodies are stored by file name
// and (device ID ...) forms are mapped to their file for lazy loading.
func (rt *Runtime) index() error {
	files, err := filepath.Glob(filepath.Join(rt.specDir, "*.lsp"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no device specifications in %s", rt.specDir)
	}
	sort.Strings(files)
	// Top-level definitions (ids, helper functions, macros) are shared by
	// every spec, so they are evaluated up front like the Engine does;
	// (device ...) bodies are evaluated when the device is first needed.
	type topForm struct {
		file string
		form *golisp.Data
	}
	var tops []topForm
	for _, f := range files {
		src, err := readSpec(f)
		if err != nil {
			return err
		}
		forms, err := golisp.ParseAll(src)
		if err != nil {
			slog.Warn("spec parse error", "file", filepath.Base(f), "err", err)
			continue
		}
		base := strings.TrimSuffix(filepath.Base(f), ".lsp")
		consts := map[string]int64{}
		for _, form := range forms {
			if !golisp.PairP(form) || !golisp.SymbolP(golisp.Car(form)) {
				tops = append(tops, topForm{f, form})
				continue
			}
			switch golisp.StringValue(golisp.Car(form)) {
			case "includable":
				rt.includables[base] = golisp.Cdr(form)
			case "define":
				tops = append(tops, topForm{f, form})
				name, val := golisp.Car(golisp.Cdr(form)), golisp.Car(golisp.Cdr(golisp.Cdr(form)))
				if golisp.SymbolP(name) && golisp.IntegerP(val) {
					consts[golisp.StringValue(name)] = golisp.IntegerValue(val)
				}
			default:
				tops = append(tops, topForm{f, form})
			case "device":
				// A device file can be (include ...)d by another device,
				// which pulls in the body of its first declaration.
				if _, ok := rt.deviceBodies[base]; !ok {
					rt.deviceBodies[base] = golisp.Cdr(golisp.Cdr(form))
				}
				idForm := golisp.Car(golisp.Cdr(form))
				var id int64
				if golisp.IntegerP(idForm) {
					id = golisp.IntegerValue(idForm)
				} else if golisp.SymbolP(idForm) {
					id = consts[golisp.StringValue(idForm)]
				}
				if id != 0 {
					if prev, dup := rt.deviceFile[uint32(id)]; dup && prev != f {
						slog.Debug("device declared in two specs", "id", fmt.Sprintf("%#x", id), "a", prev, "b", f)
					}
					rt.deviceFile[uint32(id)] = f
				}
			}
		}
	}
	for _, t := range tops {
		rt.curFile = t.file
		if _, err := rt.eval(t.form, golisp.Global); err != nil {
			rt.loadError(filepath.Base(t.file)+" "+truncate(golisp.String(t.form), 60), err)
		}
	}
	rt.curFile = ""
	slog.Info("device specifications indexed", "devices", len(rt.deviceFile), "includables", len(rt.includables))
	return nil
}

// KnownDevice reports whether a spec exists for a device id (0x1038PPPP).
func (rt *Runtime) KnownDevice(id uint32) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	_, ok := rt.deviceFile[id]
	return ok
}

// KnownIDs lists every declared device id.
func (rt *Runtime) KnownIDs() []uint32 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	ids := make([]uint32, 0, len(rt.deviceFile))
	for id := range rt.deviceFile {
		ids = append(ids, id)
	}
	return ids
}

// Device returns the loaded declaration for id, evaluating its spec file
// on first use.
func (rt *Runtime) Device(id uint32) (*Decl, error) {
	rt.mu.Lock()
	if d, ok := rt.decls[id]; ok {
		rt.mu.Unlock()
		return d, nil
	}
	file, ok := rt.deviceFile[id]
	if !ok {
		rt.mu.Unlock()
		return nil, fmt.Errorf("no specification for device %#x", id)
	}
	loaded := rt.loadedFiles[file]
	rt.loadedFiles[file] = true
	rt.mu.Unlock()
	if !loaded {
		if err := rt.loadFile(file); err != nil {
			return nil, err
		}
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if d, ok := rt.decls[id]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("specification %s did not declare device %#x", filepath.Base(file), id)
}

func (rt *Runtime) loadFile(path string) error {
	src, err := readSpec(path)
	if err != nil {
		return err
	}
	forms, err := golisp.ParseAll(src)
	if err != nil {
		return err
	}
	start := time.Now()
	rt.curFile = path
	for _, form := range forms {
		if !golisp.PairP(form) || !golisp.SymbolP(golisp.Car(form)) || golisp.StringValue(golisp.Car(form)) != "device" {
			continue
		}
		if _, err := rt.eval(form, golisp.Global); err != nil {
			slog.Warn("spec evaluation error", "file", filepath.Base(path), "err", err)
			rt.loadError(filepath.Base(path), err)
		}
	}
	slog.Debug("spec loaded", "file", filepath.Base(path), "ms", time.Since(start).Milliseconds())
	return nil
}

// eval runs golisp.Eval and converts panics in device code into errors.
func (rt *Runtime) eval(form *golisp.Data, env *golisp.SymbolTableFrame) (res *golisp.Data, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return golisp.Eval(form, env)
}

func (rt *Runtime) evalBody(body *golisp.Data, env *golisp.SymbolTableFrame) (res *golisp.Data, err error) {
	for c := body; golisp.NotNilP(c); c = golisp.Cdr(c) {
		if res, err = rt.eval(golisp.Car(c), env); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// Call invokes a function defined in a device's environment, e.g.
// (get_fw_version). Missing functions return errNoFunction.
func (d *Decl) Call(name string, args ...*golisp.Data) (*golisp.Data, error) {
	fn := d.Env.ValueOf(golisp.Intern(name))
	if golisp.NilP(fn) || !golisp.FunctionOrPrimitiveP(fn) {
		return nil, errNoFunction
	}
	return d.rt.apply(fn, golisp.ArrayToList(args), d.Env)
}

var errNoFunction = errors.New("function not defined")

// HasFunction reports whether the device defines name.
func (d *Decl) HasFunction(name string) bool {
	fn := d.Env.ValueOf(golisp.Intern(name))
	return !golisp.NilP(fn) && golisp.FunctionOrPrimitiveP(fn)
}

func (rt *Runtime) apply(fn, args *golisp.Data, env *golisp.SymbolTableFrame) (res *golisp.Data, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return golisp.ApplyWithoutEval(fn, args, env)
}

// declFor finds the device declaration whose environment encloses env.
func declFor(env *golisp.SymbolTableFrame) *Decl {
	for e := env; e != nil; e = e.Parent {
		if b, ok := e.BindingNamed(declSymbol); ok && b.Val != nil && golisp.ObjectP(b.Val) && golisp.ObjectType(b.Val) == declObjType {
			return (*Decl)(golisp.ObjectValue(b.Val))
		}
	}
	return nil
}

const (
	declSymbol  = "__gg_linux_device__"
	declObjType = "gglinux.Decl"
)

func bytesObj(b []byte) *golisp.Data {
	c := append([]byte{}, b...)
	return golisp.ObjectWithTypeAndValue("[]byte", unsafe.Pointer(&c))
}

func isBytes(d *golisp.Data) bool {
	return golisp.ObjectP(d) && golisp.ObjectType(d) == "[]byte"
}

func bytesOf(d *golisp.Data) []byte {
	if isBytes(d) {
		return *(*[]byte)(golisp.ObjectValue(d))
	}
	if golisp.StringP(d) {
		return []byte(golisp.StringValue(d))
	}
	if golisp.ListP(d) {
		var out []byte
		for c := d; golisp.NotNilP(c); c = golisp.Cdr(c) {
			e := golisp.Car(c)
			if isBytes(e) || golisp.ListP(e) {
				out = append(out, bytesOf(e)...)
			} else {
				out = append(out, byte(golisp.IntegerValue(e)))
			}
		}
		return out
	}
	return nil
}
