package devicert

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"log/slog"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/steelseries/golisp"
)

func registerBuiltins() {
	registerConstants()
	registerDSL()
	registerServiceBuiltins()
	registerByteBuiltins()
	registerMiscBuiltins()
	registerEventBuiltins()
}

// toLisp converts decoded JSON-ish Go values into golisp data, using
// frames for objects as the engine's json->lisp does.
func toLisp(v any) *golisp.Data {
	switch x := v.(type) {
	case []byte:
		return bytesObj(x)
	case nil:
		return nil
	}
	return golisp.JsonToLispWithFrames(normalize(v))
}

// normalize turns int64s into float64s for golisp's JSON converters.
func normalize(v any) any {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = normalize(e)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, e := range x {
			a[i] = normalize(e)
		}
		return a
	}
	return v
}

// fromLisp converts golisp data (frames, alists, lists, strings, json
// strings) into Go values.
func fromLisp(d *golisp.Data) any {
	switch {
	case d == nil:
		return nil
	case isBytes(d):
		b := bytesOf(d)
		a := make([]any, len(b))
		for i, x := range b {
			a[i] = int64(x)
		}
		return a
	case golisp.StringP(d):
		s := golisp.StringValue(d)
		if strings.HasPrefix(strings.TrimSpace(s), "{") {
			var v any
			if json.Unmarshal([]byte(s), &v) == nil {
				return intify(v)
			}
		}
		return s
	}
	return intify(golisp.LispWithFramesToJson(d))
}

func intify(v any) any {
	switch x := v.(type) {
	case float64:
		if x == float64(int64(x)) {
			return int64(x)
		}
		return x
	case map[string]any:
		for k, e := range x {
			x[k] = intify(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = intify(e)
		}
		return x
	}
	return v
}

func (rt *Runtime) applyTransform(fn *golisp.Data, v any) (any, error) {
	res, err := rt.apply(fn, golisp.InternalMakeList(toLisp(v)), golisp.Global)
	if err != nil {
		return nil, err
	}
	return fromLisp(res), nil
}

func lispBool(b bool) *golisp.Data { return golisp.BooleanWithValue(b) }

func jsonString(v any) *golisp.Data {
	b, _ := json.Marshal(v)
	return golisp.StringWithValue(string(b))
}

// decodeRequest accepts a JSON string, frame or alist describing
// {apiName: values}.
func decodeRequest(d *golisp.Data) (map[string]any, error) {
	v := fromLisp(d)
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected an object of api settings, got %s", golisp.String(d))
	}
	return m, nil
}

func (rt *Runtime) deviceArg(d *golisp.Data) (*Decl, error) {
	return rt.Device(uint32(golisp.IntegerValue(d)))
}

func evalArgs(a []*golisp.Data, env *golisp.SymbolTableFrame) ([]*golisp.Data, error) {
	out := make([]*golisp.Data, len(a))
	for i, x := range a {
		v, err := instance.eval(x, env)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func registerServiceBuiltins() {
	sf("write-to-device", "2|3", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		args, err := evalArgs(a, env)
		if err != nil {
			return nil, err
		}
		d, err := instance.deviceArg(args[0])
		if err != nil {
			return nil, err
		}
		req, err := decodeRequest(args[1])
		if err != nil {
			return nil, err
		}
		for name, vals := range req {
			if err := d.WriteApi(name, vals); err != nil {
				return lispBool(false), err
			}
		}
		return lispBool(true), nil
	})
	sf("read-from-device", "2", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		args, err := evalArgs(a, env)
		if err != nil {
			return nil, err
		}
		d, err := instance.deviceArg(args[0])
		if err != nil {
			return nil, err
		}
		req, err := decodeRequest(args[1])
		if err != nil {
			return nil, err
		}
		out := map[string]any{}
		for name, v := range req {
			m, _ := v.(map[string]any)
			vals, err := d.ReadApi(name, m)
			if err != nil {
				return nil, err
			}
			out[name] = vals
		}
		return jsonString(out), nil
	})
	raw := func(read bool) func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
			args, err := evalArgs(a, env)
			if err != nil {
				return nil, err
			}
			d, err := instance.deviceArg(args[0])
			if err != nil {
				return nil, err
			}
			proto := golisp.IntegerValue(args[1])
			buf := bytesOf(args[2])
			if len(buf) < 8 {
				return nil, fmt.Errorf("raw device command too short")
			}
			c := &Chunk{Type: int64(binary.LittleEndian.Uint32(buf)), Size: int64(binary.LittleEndian.Uint32(buf[4:]))}
			resp, _, err := d.execChunk(proto, c, buf[8:], nil, read, d.newIOState())
			if err != nil {
				if read {
					return nil, err
				}
				return lispBool(false), err
			}
			if !read {
				return lispBool(true), nil
			}
			return bytesObj(append(append([]byte{}, buf[:8]...), resp...)), nil
		}
	}
	golisp.MakeRestrictedSpecialForm("raw-write-to-device", "3", func(args *golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return raw(false)(list(args), env)
	})
	sf("raw-read-from-device", "3", raw(true))
	pf("device-is-connected", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := instance.deviceArg(a[0])
		if err != nil {
			return lispBool(false), nil
		}
		return lispBool(instance.transport.Connected(d.target())), nil
	})
	pf("get-devices", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) {
		var out []*golisp.Data
		for _, id := range instance.KnownIDs() {
			if d, err := instance.Device(id); err == nil && instance.transport.Connected(d.target()) {
				out = append(out, golisp.IntegerWithValue(int64(id)))
			}
		}
		return golisp.ArrayToList(out), nil
	})
	sf("rewrite-cached-values", "2", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		v, err := instance.eval(a[0], env)
		if err != nil {
			return nil, err
		}
		d, err := instance.deviceArg(v)
		if err != nil {
			return nil, err
		}
		// Specs rewrite what was deployed when a wireless device they
		// could not reach connects again; nothing deployed, nothing to do.
		api := sym(a[1])
		vals := d.WantedValues(api)
		if len(vals) == 0 {
			return lispBool(false), nil
		}
		return lispBool(d.WriteApi(api, vals) == nil), nil
	})
	sf("get-cached-values", "2", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		v, err := instance.eval(a[0], env)
		if err != nil {
			return nil, err
		}
		d, err := instance.deviceArg(v)
		if err != nil {
			return nil, err
		}
		return jsonString(map[string]any{sym(a[1]): d.WantedValues(sym(a[1]))}), nil
	})
	pf("call-device-function", "2", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := instance.deviceArg(a[0])
		if err != nil {
			return nil, err
		}
		return d.Call(sym(a[1]))
	})
	pf("call-device-function-call-error-is-nil", "2", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := instance.deviceArg(a[0])
		if err != nil {
			return nil, nil
		}
		v, err := d.Call(sym(a[1]))
		if err != nil {
			return nil, nil
		}
		return v, nil
	})
	// serialize-api-to-bytearray builds PrismSync reports (see prism.go).
	pf("serialize-api-to-bytearray", "2", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := instance.deviceArg(a[0])
		if err != nil {
			return nil, err
		}
		req, err := decodeRequest(a[1])
		if err != nil {
			return nil, err
		}
		var out []byte
		for name, v := range req {
			m, _ := v.(map[string]any)
			b, err := d.serializeForPrism(name, m)
			if err != nil {
				return nil, err
			}
			out = append(out, b...)
		}
		return bytesObj(out), nil
	})
	pf("firmware-version-of", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := instance.deviceArg(a[0])
		if err != nil {
			return golisp.StringWithValue(""), nil
		}
		v, err := d.Call("get_fw_version")
		if err != nil {
			return golisp.StringWithValue(""), nil
		}
		return v, nil
	})
	sf("stop-sequence", "2", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return nil, nil
	})
	sf("resume-sequence", "2", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return nil, nil
	})
	devFlag := func(name string, fn func(d *Decl, a []*golisp.Data) *golisp.Data) {
		pf(name, "*", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
			if len(a) == 0 {
				return nil, nil
			}
			d, err := instance.deviceArg(a[0])
			if err != nil {
				return nil, nil
			}
			return fn(d, a[1:]), nil
		})
	}
	devFlag("bust-cache", func(d *Decl, _ []*golisp.Data) *golisp.Data { d.BustCache(); return nil })
	devFlag("block-commands", func(d *Decl, _ []*golisp.Data) *golisp.Data {
		d.mu.Lock()
		d.blocked = true
		d.mu.Unlock()
		return nil
	})
	devFlag("unblock-commands", func(d *Decl, _ []*golisp.Data) *golisp.Data {
		d.mu.Lock()
		d.blocked = false
		d.mu.Unlock()
		return nil
	})
	devFlag("are-commands-blocked", func(d *Decl, _ []*golisp.Data) *golisp.Data {
		d.mu.Lock()
		defer d.mu.Unlock()
		return lispBool(d.blocked)
	})
	devFlag("set-config-sync-disabled-state", func(d *Decl, a []*golisp.Data) *golisp.Data {
		d.mu.Lock()
		d.syncDisabled = len(a) > 0 && golisp.BooleanValue(a[0])
		d.mu.Unlock()
		return nil
	})
	devFlag("set-firmware-supported", func(d *Decl, a []*golisp.Data) *golisp.Data {
		v := len(a) > 0 && golisp.BooleanValue(a[0])
		d.mu.Lock()
		d.firmwareSupported = &v
		d.mu.Unlock()
		return nil
	})
	devFlag("get-firmware-supported", func(d *Decl, _ []*golisp.Data) *golisp.Data { return lispBool(d.FirmwareSupported()) })
	devFlag("set-output-onboard-profile-schema", func(d *Decl, a []*golisp.Data) *golisp.Data {
		if len(a) > 0 {
			d.mu.Lock()
			d.outputOnboardSchem = golisp.IntegerValue(a[0])
			d.mu.Unlock()
		}
		return nil
	})
	devFlag("get-output-onboard-profile-schema", func(d *Decl, _ []*golisp.Data) *golisp.Data {
		d.mu.Lock()
		defer d.mu.Unlock()
		return golisp.IntegerWithValue(d.outputOnboardSchem)
	})
	devFlag("get-global-settings", func(d *Decl, _ []*golisp.Data) *golisp.Data {
		return jsonString(instance.hooks.ActiveConfigSettings(d.ID))
	})
	devFlag("get-factory-settings", func(d *Decl, _ []*golisp.Data) *golisp.Data { return jsonString(map[string]any{}) })
	pf("optimized-child-device-by-name", "2", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		h, ok := instance.hooks.(ChildDevices)
		if !ok || !golisp.IntegerP(a[0]) || !golisp.StringP(a[1]) {
			return lispBool(false), nil
		}
		return lispBool(h.OptimizeForChild(uint32(golisp.IntegerValue(a[0])), golisp.StringValue(a[1]))), nil
	})
	devFlag("set-onboard-profile-config", func(d *Decl, _ []*golisp.Data) *golisp.Data { return nil })
	devFlag("get-onboard-profile-config", func(d *Decl, _ []*golisp.Data) *golisp.Data { return golisp.StringWithValue("") })
	for _, name := range []string{"register-mm-device-info", "register-mm-device-info-and-send"} {
		n := name
		pf(n, "3", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
			var features int64
			for _, f := range golisp.ToArray(a[2]) {
				features |= golisp.IntegerValue(f)
			}
			instance.hooks.DeviceEvent(uint32(golisp.IntegerValue(a[0])), "mmDeviceInfo", map[string]any{
				"audioDevice": golisp.IntegerValue(a[1]), "features": features,
			})
			return nil, nil
		})
	}
	pf("request-sonar-status-refresh", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	pf("get-sub-app-action-v2-value", "1", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	pf("get-sonar-status-via-saa", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return jsonString(map[string]any{"enabled": false}), nil
	})
	var atomics sync.Map
	pf("set-global-atomic", "2", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		atomics.Store(golisp.String(a[0]), a[1])
		return a[1], nil
	})
	pf("load-global-atomic", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		v, _ := atomics.Load(golisp.String(a[0]))
		d, _ := v.(*golisp.Data)
		return d, nil
	})
	sf("app", "*", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	pf("bind-current-device-declaration", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	pf("self-external-software-registry-key-specific-value", "2", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	pf("self-background-software", "2", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	pf("device-event", "3", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		data, _ := fromLisp(a[2]).(map[string]any)
		kind := golisp.IntegerValue(a[1])
		instance.hooks.DeviceEvent(uint32(golisp.IntegerValue(a[0])), strconv.FormatInt(kind, 10), data)
		return nil, nil
	})
	pf("connected-device-ids", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return golisp.Eval(golisp.InternalMakeList(golisp.Intern("get-devices")), golisp.Global)
	})
	pf("get-config-undeployed-settings", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return jsonString(map[string]any{}), nil
	})
	for _, n := range []string{"get-active-config-settings", "get-active-config-undeployed-settings"} {
		pf(n, "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
			return jsonString(instance.hooks.ActiveConfigSettings(uint32(golisp.IntegerValue(a[0])))), nil
		})
	}
	pf("supports-prism-sync", "1", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		if d := declFor(env); d != nil {
			d.SupportsPrismSync = golisp.BooleanValue(a[0])
		}
		return nil, nil
	})
	pf("read-engine-file", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		rel := golisp.StringValue(a[0])
		p := filepath.Join(instance.engineDir, filepath.FromSlash(rel))
		if !strings.HasPrefix(filepath.Clean(p), filepath.Clean(instance.engineDir)) {
			return nil, fmt.Errorf("read-engine-file: path escapes engine dir")
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		return bytesObj(b), nil
	})
	pf("dump-devices", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	pf("optimize", "0|1", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	pf("cached", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) { return a[0], nil })
	pf("serialize", "2", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) { return a[1], nil })
	pf("unbind-current-declaration", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	pf("dump-struct", "1", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	pf("dump-expanded", "1", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
}

func registerByteBuiltins() {
	le := func(name string, n int) {
		pf(name, "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
			v := uint64(golisp.IntegerValue(a[0]))
			b := make([]byte, n)
			for i := 0; i < n; i++ {
				b[i] = byte(v >> (8 * i))
			}
			return bytesObj(b), nil
		})
	}
	for _, n := range []string{"uint32-to-bytes", "uint32->bytes", "signed-int32-to-bytes", "int32->bytes"} {
		le(n, 4)
	}
	for _, n := range []string{"uint16-to-bytes", "uint16->bytes", "signed-int16-to-bytes", "int16->bytes"} {
		le(n, 2)
	}
	for _, n := range []string{"uint32-to-uint8-bytes", "uint32->uint8-bytes"} {
		pf(n, "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
			return bytesObj([]byte{byte(golisp.IntegerValue(a[0]))}), nil
		})
	}
	for _, n := range []string{"uint32-to-uint16-bytes", "uint32->uint16-bytes"} {
		le(n, 2)
	}
	from := func(name string, n int, signed bool) {
		pf(name, "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
			b := bytesOf(a[0])
			var v uint64
			for i := 0; i < n && i < len(b); i++ {
				v |= uint64(b[i]) << (8 * i)
			}
			if signed {
				shift := 64 - 8*n
				return golisp.IntegerWithValue(int64(v<<shift) >> shift), nil
			}
			return golisp.IntegerWithValue(int64(v)), nil
		})
	}
	from("uint16-from-bytes", 2, false)
	from("bytes->uint16", 2, false)
	from("uint32-from-bytes", 4, false)
	from("bytes->uint32", 4, false)
	from("bytes->int32", 4, true)
	from("bytes->signed-int16", 2, true)
	for _, n := range []string{"bytes-to-string", "bytes->string"} {
		pf(n, "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
			b := bytesOf(a[0])
			if i := strings.IndexByte(string(b), 0); i >= 0 {
				b = b[:i]
			}
			return golisp.StringWithValue(string(b)), nil
		})
	}
	pf("string->bytearray", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return bytesObj([]byte(golisp.StringValue(a[0]))), nil
	})
	pf("decimal-to-hex-string", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return golisp.StringWithValue(strconv.FormatInt(golisp.IntegerValue(a[0]), 16)), nil
	})
	pf("stm32-crc", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return golisp.IntegerWithValue(int64(stm32CRC(bytesOf(a[0])))), nil
	})
	pf("nordic-crc", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return golisp.IntegerWithValue(int64(crc32.ChecksumIEEE(bytesOf(a[0])))), nil
	})
	pf("gen-uuid-bytes", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) {
		b := make([]byte, 16)
		rand.Read(b)
		b[6] = b[6]&0x0f | 0x40
		b[8] = b[8]&0x3f | 0x80
		return bytesObj(b), nil
	})
	pf("gen-uuid-string", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) {
		b := make([]byte, 16)
		rand.Read(b)
		b[6] = b[6]&0x0f | 0x40
		b[8] = b[8]&0x3f | 0x80
		return golisp.StringWithValue(uuidString(b)), nil
	})
	pf("uuid-bytes->string", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return golisp.StringWithValue(uuidString(bytesOf(a[0]))), nil
	})
	pf("uuid-string->bytes", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		b, err := hex.DecodeString(strings.ReplaceAll(golisp.StringValue(a[0]), "-", ""))
		return bytesObj(b), err
	})
	pf("merge-lists-with-offsets-over-bytearray", "3|4|5", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		base := append([]byte{}, bytesOf(a[0])...)
		lists, offsets := golisp.ToArray(a[1]), golisp.ToArray(a[2])
		for i, l := range lists {
			if i >= len(offsets) {
				break
			}
			off := int(golisp.IntegerValue(offsets[i]))
			for j, x := range bytesOf(l) {
				if off+j < len(base) {
					base[off+j] = x
				}
			}
		}
		return bytesObj(base), nil
	})
	pf("merge-multi-write-buffers", "*", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		var out []byte
		for _, x := range a {
			out = append(out, bytesOf(x)...)
		}
		return bytesObj(out), nil
	})
	columnPacked := func(lsb, flipY bool) func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
			px, w, h := bytesOf(a[0]), int(golisp.IntegerValue(a[1])), int(golisp.IntegerValue(a[2]))
			return bytesObj(columnPack(px, w, h, lsb, flipY)), nil
		}
	}
	pf("convert-to-column-packed-byte-format", "3", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		px, w, h := bytesOf(a[0]), int(golisp.IntegerValue(a[1])), int(golisp.IntegerValue(a[2]))
		return bytesObj(pagePack(px, w, h)), nil
	})
	pf("convert-to-column-packed-byte-format-with-LSB", "3", columnPacked(true, false))
	pf("convert-to-column-packed-byte-format-with-LSB-y-flipped", "3", columnPacked(true, true))
}

// pagePack is the Engine's convert-to-column-packed-byte-format (the 2024
// Apex screens): a picture packed by rows, eight pixels a byte with the
// leftmost in the high bit, repacked by pages of eight rows: byte
// (y/8)*w+x, the top pixel in the low bit. A picture of a byte a pixel
// is taken too.
func pagePack(px []byte, w, h int) []byte {
	out := make([]byte, w*((h+7)/8))
	on := func(x, y int) bool {
		i := y*w + x
		if len(px) >= w*h {
			return px[i] != 0
		}
		return i>>3 < len(px) && px[i>>3]&(0x80>>uint(i&7)) != 0
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if on(x, y) {
				out[(y/8)*w+x] |= 1 << uint(y%8)
			}
		}
	}
	return out
}

// columnPack converts a 1-byte-per-pixel monochrome bitmap into the
// column-major, 8-pixels-per-byte layout OLED screens take.
func columnPack(px []byte, w, h int, lsb, flipY bool) []byte {
	pages := (h + 7) / 8
	out := make([]byte, w*pages)
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			sy := y
			if flipY {
				sy = h - 1 - y
			}
			if sy*w+x >= len(px) || px[sy*w+x] == 0 {
				continue
			}
			bit := uint(y % 8)
			if !lsb {
				bit = 7 - bit
			}
			out[x*pages+y/8] |= 1 << bit
		}
	}
	return out
}

func uuidString(b []byte) string {
	if len(b) < 16 {
		return ""
	}
	h := hex.EncodeToString(b[:16])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// stm32CRC is the STM32 hardware CRC: CRC-32/MPEG-2 over 32-bit words.
func stm32CRC(b []byte) uint32 {
	crc := uint32(0xffffffff)
	for i := 0; i+4 <= len(b); i += 4 {
		crc ^= binary.LittleEndian.Uint32(b[i:])
		for j := 0; j < 32; j++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func registerMiscBuiltins() {
	sf("devour-but-log-error", "1|2", devour)
	sf("devour-but-log-panic", "1|2", devour)
	sf("ignore-devicelib-error", ">=2", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		v, _ := instance.evalBody(golisp.ArrayToList(a[1:]), env)
		return v, nil
	})
	pf("map-with-nil-on-error", ">=1", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		if len(a) < 2 {
			return nil, nil
		}
		var out []*golisp.Data
		for _, e := range golisp.ToArray(a[1]) {
			v, err := instance.apply(a[0], golisp.InternalMakeList(e), env)
			if err != nil {
				v = nil
			}
			out = append(out, v)
		}
		return golisp.ArrayToList(out), nil
	})
	pf("rand-intn", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		n := golisp.IntegerValue(a[0])
		if n <= 0 {
			return golisp.IntegerWithValue(0), nil
		}
		return golisp.IntegerWithValue(mrand.Int64N(n)), nil
	})
	pf("exec", "2", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		// Device scripts only use this for Windows helpers; never run
		// external programs from device code on Linux.
		slog.Debug("exec from device code ignored", "cmd", golisp.String(a[0]))
		return golisp.StringWithValue(""), nil
	})
	pf("write-log-if-debug-logging-enabled", "*", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		if d := declFor(env); d != nil && len(a) > 0 {
			var parts []string
			for _, x := range a {
				parts = append(parts, golisp.String(x))
			}
			instance.hooks.Log(d.ID, strings.Join(parts, " "))
		}
		return nil, nil
	})
	pf("semver-check-range", "2", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return lispBool(semverInRange(golisp.StringValue(a[0]), golisp.StringValue(a[1]))), nil
	})
	for _, n := range []string{"alist-to-json", "alist->json"} {
		pf(n, "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
			return golisp.StringWithValue(golisp.LispToJsonString(a[0])), nil
		})
	}
	for _, n := range []string{"json-to-alist", "json->alist"} {
		pf(n, "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
			s := golisp.StringValue(a[0])
			var v any
			if err := json.Unmarshal([]byte(s), &v); err != nil {
				return nil, fmt.Errorf("json->alist: %w", err)
			}
			return golisp.JsonToLisp(v), nil
		})
	}
	pf("is-64-bit", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return lispBool(true), nil })
	pf("env-var", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return golisp.StringWithValue(os.Getenv(golisp.StringValue(a[0]))), nil
	})
	pf("platform-supports-uwp-apo", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return lispBool(false), nil })
	pf("beta-firmware-optin", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return lispBool(false), nil })
	pf("beta-firmware-path", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) { return a[0], nil })
	pf("chid-is-in-list", "1", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return lispBool(false), nil })
	pf("get-os-keyboard-region", "0", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return golisp.StringWithValue(osKeyboardRegion()), nil
	})
	var layoutMu sync.Mutex
	layouts := map[string]*golisp.Data{}
	pf("init-keyboard-layout-fallback", "3", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		layoutMu.Lock()
		defer layoutMu.Unlock()
		k := golisp.String(a[0])
		if _, ok := layouts[k]; !ok {
			layouts[k] = a[1]
		}
		return layouts[k], nil
	})
	pf("get-keyboard-layout-fallback", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		layoutMu.Lock()
		defer layoutMu.Unlock()
		return layouts[golisp.String(a[0])], nil
	})
	pf("set-keyboard-layout-fallback", "2", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		layoutMu.Lock()
		defer layoutMu.Unlock()
		layouts[golisp.String(a[0])] = a[1]
		return nil, nil
	})
	// (convert-onboard-macro-bindings pid config mappings triggers size fn)
	// gives (mappings macro-data): the keys bound to macros in the
	// configuration (config, or the device's current one when "") point at
	// onboard macro function fn, and those macros are packed into size
	// bytes (see macros.go). The configuration "NONE" has no macros.
	pf("convert-onboard-macro-bindings", "6", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		mappings, triggers := a[2], a[3]
		if !golisp.ListP(mappings) || !golisp.ListP(triggers) {
			return nil, fmt.Errorf("convert-onboard-macro-bindings expects lists of mappings and triggers")
		}
		if golisp.Length(mappings) != golisp.Length(triggers) {
			return nil, fmt.Errorf("convert-onboard-macro-bindings: %d mappings for %d triggers", golisp.Length(mappings), golisp.Length(triggers))
		}
		if !golisp.IntegerP(a[4]) || golisp.IntegerValue(a[4]) < 0 || !golisp.IntegerP(a[5]) {
			return nil, fmt.Errorf("convert-onboard-macro-bindings expects a macro data size and function, got %s %s", golisp.String(a[4]), golisp.String(a[5]))
		}
		size, fn := int(golisp.IntegerValue(a[4])), uint8(golisp.IntegerValue(a[5]))
		config := ""
		if golisp.StringP(a[1]) {
			config = golisp.StringValue(a[1])
		}
		var bindings []MacroBinding
		if src, ok := instance.hooks.(MacroSource); ok && golisp.IntegerP(a[0]) && config != "NONE" {
			bindings = src.MacroBindings(uint32(golisp.IntegerValue(a[0])), config)
		}
		if len(bindings) == 0 {
			return golisp.InternalMakeList(mappings, bytesObj(make([]byte, size))), nil
		}
		maps, err := keyMappings(mappings)
		if err != nil {
			return nil, fmt.Errorf("convert-onboard-macro-bindings: %w", err)
		}
		trigs, err := bindingTriggers(triggers)
		if err != nil {
			return nil, fmt.Errorf("convert-onboard-macro-bindings: %w", err)
		}
		out, data, skipped := ConvertOnboardMacros(maps, trigs, bindings, size, fn)
		for _, e := range skipped {
			slog.Warn("onboard macro left out", "err", e)
		}
		list := make([]*golisp.Data, len(out))
		for i, m := range out {
			list[i] = toLisp(map[string]any{
				"hid_code":  int64(m.HIDCode),
				"function":  int64(m.Function),
				"key_codes": []any{int64(m.KeyCodes[0]), int64(m.KeyCodes[1]), int64(m.KeyCodes[2]), int64(m.KeyCodes[3])},
			})
		}
		return golisp.InternalMakeList(golisp.ArrayToList(list), bytesObj(data)), nil
	})
	pf("if-deploying", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return lispBool(golisp.IntegerP(a[0]) && instance.hooks.Deploying(uint32(golisp.IntegerValue(a[0])))), nil
	})

	// Windows registry and external-software checks: nothing to find on
	// Linux.
	for _, n := range []string{"require-registry", "software-registry", "software-url", "requires-external-software", "external-software-registry-key", "external-software-registry-key-value", "external-software-url"} {
		pf(n, "*", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	}
	for _, n := range []string{"require-registry-value", "software-registry-value", "external-software-registry-key-specific-value", "registry-key-exists", "registry-value-exists"} {
		pf(n, "*", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return lispBool(false), nil })
	}
	sf("registry-get", "3", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	sf("require-device", "1", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	sf("pipe", "*", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	for _, n := range []string{"connection-filter-add-predicate-vidpid", "connection-filter-remove-predicate-vidpid", "connection-filter-remove-predicate-by-id"} {
		pf(n, "*", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	}
	sf("configuration-migration", "*", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	sf("migration-schema", "*", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	pf("multiline-text-to-rgba-image-data", "6", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return bytesObj(nil), nil })
	pf("get-bw-bitpacked-icon-image-reverse-bytepacking-from-rgba-image-data", "1", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return bytesObj(nil), nil
	})
	// PrismSync lighting hooks (driven by the Linux PrismSync service).
	// prism-sync-make-device-rewrite re-applies PrismSync's lighting to a
	// device that reconnects (an Aerox mouse waking up); without PrismSync
	// there is nothing to re-apply.
	for _, n := range []string{"prism-sync-actuation_level", "pause-prism-sync", "resume-prism-sync", "prism-sync-make-device-rewrite"} {
		pf(n, "*", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	}
	// The zone builtins return their own form, so zone lists a spec builds
	// read the same as quoted ones (see prismZoneForm).
	for _, n := range []string{"prism-sync-zone", "prism-sync-zone-bitmap-hid-code", "prism-sync-zone-linear"} {
		sym := golisp.Intern(n)
		pf(n, "*", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
			return golisp.Cons(sym, golisp.ArrayToList(a)), nil
		})
	}
	sf("pause-prism-sync-for-block", "*", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		if len(a) < 2 {
			return nil, nil
		}
		return instance.evalBody(golisp.ArrayToList(a[1:]), env)
	})
}

func devour(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
	v, err := instance.eval(a[0], env)
	if err != nil {
		slog.Debug("device code error (ignored)", "err", err)
		if len(a) > 1 {
			return instance.eval(a[1], env)
		}
		return nil, nil
	}
	return v, nil
}

func osKeyboardRegion() string {
	lang := os.Getenv("LANG")
	switch {
	case strings.HasPrefix(lang, "en_GB"):
		return "UK"
	case strings.HasPrefix(lang, "de"):
		return "DE"
	case strings.HasPrefix(lang, "fr"):
		return "FR"
	case strings.HasPrefix(lang, "ja"):
		return "JP"
	case strings.HasPrefix(lang, "ko"):
		return "KR"
	case strings.HasPrefix(lang, "es"):
		return "ES"
	}
	return "US"
}

func registerConstants() {
	bind := func(m map[string]int64) {
		for k, v := range m {
			golisp.Global.BindToProtected(golisp.Intern(k), golisp.IntegerWithValue(v))
		}
	}
	bind(map[string]int64{"HID": 0, "CNXT": 1, "SSE": 2, "READCONST": 3, "PROGRAM": 123, "CNXT_DTSHPX": 4, "MMDEVICECMD": 6, "SSHZ_DTSHPX": 10, "SSHZ_DTSHPXV2": 11, "AVNERA": 8, "BERENA": 9})
	bind(map[string]int64{"HIDIO": 0, "HIDNOW": 2, "HIDCONFIG": 5, "HIDFEATURE": 1, "HIDREADONLY": 3, "HIDFEATUREREADONLY": 7, "HIDFEATUREOUTFEATUREIN": 15, "HIDFEATUREOUTINPUTIN": 14, "HIDOUTPUTFEATUREIN": 16, "HIDSLEEP": 4, "CNXT_OWB_WRITE": 8, "CNXT_OWB_READ": 9, "SSFWSTATUS": 10, "HIDIONOFAIL": 11, "SS_OWB_WRITE": 12})
	bind(map[string]int64{"DRIVERCMD": 1, "SSFW": 3, "SSFWLEGACY": 13, "MOUSEACCELDECEL": 6, "MOUSEACCELV2": 44, "MOUSESMOOTHING": 7, "MOUSEROTATION": 43, "ACTIVECPI": 9, "CPIVALUES": 8, "CPITOGGLEBUTTONMASK": 10, "XYCPIVALUES": 41, "CLEARMACROS": 5, "CLEARSSECOMMANDS": 11, "SSFWEX": 15, "GAMEPADANALOGSTICK": 17, "BUTTON": 18, "GAMEPADTRIGGER": 19, "HOLTEKFW": 21, "SSFWGENERIC": 22, "SSECOMMAND_SLEEP": 2, "SSFWKLC": 23, "SSFWKLCV2": 27, "SETWINDOWSCPI": 25, "FAKEWRITE": 28, "SSFWZ6": 29, "SSFWFIZZ": 31, "SSFWWITHMODE": 30, "SSFWSTRATUSPLUS": 32, "SSFWNOVAPROWIRELESSMCUTX": 33, "SSFWNOVAPROWIRELESSDSP": 34, "SSFWNOVAPROWIRELESSMCURX": 35, "SSFWNOVAPROWIRELESSQCC": 36, "SSFWFIZZWITHMODE": 37, "SSFWALIAS": 38, "SSFWFIZZFEATURE": 39, "SSFWARCTISNOVA7GEN2": 40, "SSFWBATCHFIZZ": 42, "SSFWSENSEIRIVALPRO": 45})
	bind(map[string]int64{"FA_READONLY": 1, "FA_WRITEONLY": 2, "FA_READWRITE": 3})
	bind(map[string]int64{"DTSHPXONOFF": 0, "DTSHPXMODE": 1, "DTSHPX_SLEEP": 2, "DTSHPXV2_STEREOMODE": 1, "DTSHPXV2_ROOMMODEL": 2, "DTSHPXV2_BASSENHANCEMENT": 3, "DTSHPXV2_DIALOGUEENHANCEMENT": 4, "DTSHPXV2_STEREOMODE_TRADITIONAL": 3, "DTSHPXV2_STEREOMODE_FRONT": 1, "DTSHPXV2_STEREOMODE_WIDE": 2, "DTSHPXV2_ROOMMODEL_ENTERTAINMENT": 0, "DTSHPXV2_ROOMMODEL_GAME": 1, "DTSHPXV2_ROOMMODEL_SPORTS": 2, "DTSHPXV2_OPMODE_OFF": 0, "DTSHPXV2_OPMODE_GAME": 1})
	bind(map[string]int64{"CNXTCMD": 0, "FWFILE": 1, "V2FWFILE": 2, "CX2089XFWFILENAME": 3, "CNXT_SLEEP": 4, "FWWITHDEVINFO": 5})
	bind(map[string]int64{"FW_STARTED": 0, "FW_PROGRESS": 1, "FW_FINISHED": 2, "FW_FAILED": 3})
	bind(map[string]int64{"DISPATCHTOUI": 1, "PROCESSINCORE": 2, "PROCESSINCOREASYNC": 4, "SYNC_DATA": 8, "UPDATE_GENERIC_STATUS_PROPERTIES": 16})
	bind(map[string]int64{"QUERYDEVICE": 1, "MMDEVICE_RENDER": 0, "MMDEVICE_CAPTURE": 1, "MMDEVICE_ALL": 2})
	bind(map[string]int64{"LIGHTXDEBUG": 0, "LIGHTXFW": 1, "LIGHTXRECOVER": 2, "LIGHTXIO": 3, "VMIRXMCU": 6, "XBOXMCU": 7, "VMIFW": 4, "VMIPAIR": 5})
	bind(map[string]int64{"AVIOREAD": 0, "AVIOWRITE": 1, "AVIOLONGREAD": 2, "AVIOLONGWRITE": 3})
	bind(map[string]int64{"BERENAFW": 0, "BERENAUSB": 0, "BERENADSP1": 1, "BERENADSP2": 2, "BERENAMCU1": 3, "BERENAMCU2": 4, "BERENAMCU3": 5})
	bind(map[string]int64{"CIETYPENONE": 0, "CIETYPECPIONLY": 1, "CIETYPEALWAYS": 2})
	bind(map[string]int64{"FAKE_META_LAYER_PAGE": 254, "FAKE_DUAL_ACTUATION_PAGE": 252})
	bind(map[string]int64{"COORDINATE_PLANE_DESK": 0, "COORDINATE_PLANE_BACK_WALL_ABOVE_DESK": 1, "COORDINATE_PLANE_BACK_WALL_BELOW_DESK": 4})
	// Multimedia device feature flags (values only need to be distinct).
	bind(map[string]int64{"MM_DEVICE_FEATURE_SOFTWARE_CHATMIX_FINITE": 1, "MM_DEVICE_FEATURE_SOFTWARE_CHATMIX_INFINITE": 2, "MM_DEVICE_FEATURE_DEVICE_FEATURE_SONAR_FW_UPDATE_REQUIRED": 4, "MM_DEVICE_FEATURE_DEVICE_FEATURE_SONAR_ORNSTEIN": 8, "MM_DEVICE_FEATURE_DEVICE_FEATURE_SONAR_SMOUGH": 16, "MM_DEVICE_FEATURE_DEVICE_FEATURE_SONAR_CIARAN": 32, "MM_DEVICE_FEATURE_SONAR_INTEGRATED_DEVICE": 64})
	golisp.Global.BindToProtected(golisp.Intern("__PLATFORM__"), golisp.StringWithValue("linux"))
}
