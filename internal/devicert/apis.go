package devicert

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/steelseries/golisp"
)

// Protocols (InitProtocolConstants).
const (
	ProtoHID       = 0
	ProtoCNXT      = 1
	ProtoSSE       = 2
	ProtoReadConst = 3
	ProtoMMDevice  = 6
	ProtoAvnera    = 8
	ProtoBerena    = 9
	ProtoProgram   = 123
)

// HID chunk types (InitHIDCommandChunkTypeConstants).
const (
	HIDIO                 = 0
	HIDFeature            = 1
	HIDNow                = 2
	HIDReadOnly           = 3
	HIDSleep              = 4
	HIDConfig             = 5
	HIDFeatureReadOnly    = 7
	HIDSSFWStatus         = 10
	HIDIONoFail           = 11
	HIDFeatureOutInputIn  = 14
	HIDFeatureOutFeatIn   = 15
	HIDOutputFeatureIn    = 16
	sseCommandSleep       = 2
	defaultTimeBetweenCmd = 10 * time.Millisecond
)

func registerApiForms() {
	sf("api", "*", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := currentDecl(env)
		if err != nil {
			return nil, err
		}
		api, err := buildApi(d, sym(a[0]), a[1:], env, nil)
		if err != nil {
			return nil, err
		}
		d.mu.Lock()
		d.Apis[api.Name] = api
		d.mu.Unlock()
		return nil, nil
	})
	sf("self-api", "*", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return golisp.Eval(golisp.Cons(golisp.Intern("api"), golisp.ArrayToList(a)), env)
	})
	sf("derived-api", "2", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := currentDecl(env)
		if err != nil {
			return nil, err
		}
		base := d.Api(sym(a[1]))
		if base == nil {
			return nil, fmt.Errorf("derived-api: unknown api %s", sym(a[1]))
		}
		c := *base
		c.Name = sym(a[0])
		c.derivedFrom = base.Name
		d.mu.Lock()
		d.Apis[c.Name] = &c
		d.mu.Unlock()
		return nil, nil
	})
	var parseCommand func(a []*golisp.Data, env *golisp.SymbolTableFrame) ([]*ApiCommand, error)
	parseCommand = func(a []*golisp.Data, env *golisp.SymbolTableFrame) ([]*ApiCommand, error) {
		// Multi-protocol form: (api-write (HID chunks...) (SSE chunks...)).
		if len(a) > 0 && golisp.PairP(a[0]) && golisp.SymbolP(golisp.Car(a[0])) {
			if v := env.ValueOf(golisp.Car(a[0])); golisp.IntegerP(v) {
				var cmds []*ApiCommand
				for _, sub := range a {
					c, err := parseCommand(golisp.ToArray(sub), env)
					if err != nil {
						return nil, err
					}
					cmds = append(cmds, c...)
				}
				return cmds, nil
			}
		}
		proto, err := evalInt(a, 0, env)
		if err != nil {
			return nil, err
		}
		cmd := &ApiCommand{Protocol: proto}
		var cur *ChunkGroup
		for _, f := range a[1:] {
			v, err := instance.eval(f, env)
			if err != nil {
				return nil, err
			}
			for _, m := range collectMarkers(v) {
				switch m.kind {
				case "chunk":
					if cur == nil {
						cur = &ChunkGroup{}
						cmd.Groups = append(cmd.Groups, cur)
					}
					cur.Chunks = append(cur.Chunks, m.val.(*Chunk))
				case "chunk-loop":
					cmd.Groups = append(cmd.Groups, m.val.(*ChunkGroup))
					cur = nil
				}
			}
		}
		return []*ApiCommand{cmd}, nil
	}
	command := func(kind string) func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
			cmds, err := parseCommand(a, env)
			if err != nil {
				return nil, err
			}
			return mk(kind, cmds), nil
		}
	}
	sf("api-read", "*", command("api-read"))
	sf("api-write", "*", command("api-write"))
	chunk := func(read, write bool) func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
			t, err := evalInt(a, 0, env)
			if err != nil {
				return nil, err
			}
			size, err := evalInt(a, 1, env)
			if err != nil {
				return nil, err
			}
			var code *golisp.Data
			if len(a) > 2 {
				code = a[2]
			}
			return mk("chunk", &Chunk{Type: t, Size: size, Code: code, SetReadBit: read, WriteOnly: write}), nil
		}
	}
	sf("chunk", "3", chunk(false, false))
	sf("read-chunk", "3", chunk(true, false))
	sf("write-chunk", "3", chunk(false, true))
	sf("chunk-loop", "*", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		g := &ChunkGroup{LoopCount: a[0]}
		for _, f := range a[1:] {
			v, err := instance.eval(f, env)
			if err != nil {
				return nil, err
			}
			for _, m := range collectMarkers(v) {
				if m.kind == "chunk" {
					g.Chunks = append(g.Chunks, m.val.(*Chunk))
				}
			}
		}
		return mk("chunk-loop", g), nil
	})
	code := func(kind string) func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
			return mk(kind, golisp.ArrayToList(a)), nil
		}
	}
	for _, k := range []string{"api-let", "api-let*", "api-on-read-success", "api-on-write-success", "api-on-read-failure", "api-on-write-failure", "api-finally"} {
		sf(k, "*", code(k))
	}
	flag := func(kind string) func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return mk(kind, nil), nil }
	}
	pf("cache-exempt", "0", flag("cache-exempt"))
	pf("unblockable", "0", flag("unblockable"))
	pf("no-preempt", "0", flag("no-preempt"))
	pf("mixes-reads-writes", "0", flag("mixes-reads-writes"))
	pf("is-firmware-update", "0", flag("is-firmware-update"))
	pf("api-hold-after-write", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return mk("hold", golisp.IntegerValue(a[0])), nil
	})
	sf("read-chunks-byte-specifiers", "1", func(a []*golisp.Data, _ *golisp.SymbolTableFrame) (*golisp.Data, error) {
		return mk("read-specifiers", a[0]), nil
	})
	sf("api-readback-error-byte", "2", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		p, err := evalInt(a, 0, env)
		if err != nil {
			return nil, err
		}
		v, err := evalInt(a, 1, env)
		return mk("readback-err", [2]int64{p, v}), err
	})
}

func buildApi(d *Decl, name string, body []*golisp.Data, env *golisp.SymbolTableFrame, base *Api) (*Api, error) {
	api := &Api{Name: name, Env: env, declaredInDecl: d}
	for _, form := range body {
		v, err := instance.eval(form, env)
		if err != nil {
			return nil, fmt.Errorf("api %s: %w", name, err)
		}
		for _, m := range collectMarkers(v) {
			switch m.kind {
			case "api-read":
				cmds := m.val.([]*ApiCommand)
				api.Read = cmds[0]
				for _, c := range cmds {
					if c.Protocol == ProtoHID {
						api.Read = c
					}
				}
			case "api-write":
				api.Write = append(api.Write, m.val.([]*ApiCommand)...)
			case "api-let":
				api.Let = m.val.(*golisp.Data)
			case "api-let*":
				api.Let, api.LetStar = m.val.(*golisp.Data), true
			case "api-on-read-success":
				api.OnReadSuccess = m.val.(*golisp.Data)
			case "api-on-write-success":
				api.OnWriteSuccess = m.val.(*golisp.Data)
			case "api-on-read-failure":
				api.OnReadFailure = m.val.(*golisp.Data)
			case "api-on-write-failure":
				api.OnWriteFailure = m.val.(*golisp.Data)
			case "api-finally":
				api.Finally = m.val.(*golisp.Data)
			case "cache-exempt":
				api.CacheExempt = true
			case "unblockable":
				api.Unblockable = true
			case "no-preempt":
				api.NoPreempt = true
			case "mixes-reads-writes":
				api.MixesReadsWrites = true
			case "is-firmware-update":
				api.IsFirmwareUpdate = true
			case "hold":
				api.HoldAfterWriteMs = int(m.val.(int64))
			case "read-specifiers":
				api.ReadChunksSpecFn = m.val.(*golisp.Data)
			case "readback-err":
				r := m.val.([2]int64)
				api.ReadbackErrByte = &r
			}
		}
	}
	return api, nil
}

// ------------------------------------------------------------- execution

// ErrFirmwareDisabled is returned for firmware update APIs unless the
// runtime was started with AllowFirmware.
var ErrFirmwareDisabled = errors.New("firmware updates are disabled")

type ioState struct {
	timeBetween time.Duration
	timeout     time.Duration
	lastWrite   time.Time
}

func (d *Decl) target() Target {
	return Target{ProductID: uint16(d.ID & 0xffff), UsagePage: d.UsagePage, Usage: d.Usage, Interface: d.InterfaceNum}
}

func (d *Decl) apiEnv(api *Api, payload *golisp.Data) (*golisp.SymbolTableFrame, error) {
	base := api.Env
	if base == nil {
		base = d.Env
	}
	env := golisp.NewSymbolTableFrameBelow(base, "api "+api.Name)
	env.BindLocallyTo(golisp.Intern("payload"), payload)
	if api.Let != nil {
		for _, b := range golisp.ToArray(api.Let) {
			if !golisp.PairP(b) {
				continue
			}
			bindEnv := env
			if !api.LetStar {
				bindEnv = env
			}
			v, err := d.rt.evalBody(golisp.Cdr(b), bindEnv)
			if err != nil {
				return nil, fmt.Errorf("api-let %s: %w", golisp.String(golisp.Car(b)), err)
			}
			env.BindLocallyTo(golisp.Car(b), v)
		}
	}
	return env, nil
}

type chunkResult struct {
	data   []byte
	isRead bool
}

// runCommand executes all chunk groups of one api-read/api-write; reads
// says which chunks read the device's reply.
func (d *Decl) runCommand(cmd *ApiCommand, env *golisp.SymbolTableFrame, st *ioState, payload []byte, reads func(*Chunk) bool) ([]chunkResult, error) {
	var results []chunkResult
	for _, g := range cmd.Groups {
		count := int64(1)
		if g.LoopCount != nil {
			v, err := d.rt.eval(g.LoopCount, env)
			if err != nil {
				return results, fmt.Errorf("chunk-loop count: %w", err)
			}
			count = golisp.IntegerValue(v)
		}
		for i := int64(0); i < count; i++ {
			if g.LoopCount != nil {
				env.BindLocallyTo(golisp.Intern("loopcount"), golisp.IntegerWithValue(i))
			}
			for _, c := range g.Chunks {
				var data []byte
				if c.Code != nil {
					v, err := d.rt.eval(c.Code, env)
					if err != nil {
						return results, fmt.Errorf("chunk data: %w", err)
					}
					data = bytesOf(v)
				}
				resp, isRead, err := d.execChunk(cmd.Protocol, c, data, payload, reads(c), st)
				if err != nil {
					return results, err
				}
				results = append(results, chunkResult{resp, isRead})
			}
		}
	}
	return results, nil
}

// execChunk runs one chunk. read says whether a HID chunk reads the
// device's reply: Engine's DeviceLib reads replies for api-read, and for
// the read-chunks of an api that mixes reads and writes; every other chunk
// of an api-write only sends its report.
func (d *Decl) execChunk(proto int64, c *Chunk, data, payload []byte, read bool, st *ioState) ([]byte, bool, error) {
	switch proto {
	case ProtoReadConst:
		return payload, true, nil
	case ProtoHID:
		switch c.Type {
		case HIDSleep:
			if len(data) >= 4 {
				time.Sleep(time.Duration(binary.LittleEndian.Uint32(data)) * time.Millisecond)
			}
			return nil, false, nil
		case HIDConfig:
			if len(data) >= 12 {
				st.timeBetween = time.Duration(binary.LittleEndian.Uint32(data[0:])) * time.Millisecond
				st.timeout = time.Duration(binary.LittleEndian.Uint32(data[8:])) * time.Millisecond
			}
			return nil, false, nil
		case HIDSSFWStatus:
			return nil, false, nil
		}
		if wait := st.timeBetween - time.Since(st.lastWrite); wait > 0 {
			time.Sleep(wait)
		}
		resp, err := d.rt.transport.Chunk(d.target(), int(c.Type), int(c.Size), data, read, st.timeBetween, st.timeout)
		st.lastWrite = time.Now()
		if err != nil && c.Type == HIDIONoFail {
			err = nil
		}
		isRead := read && c.Type != HIDNow && !c.WriteOnly
		return resp, isRead, err
	case ProtoSSE:
		// SSE commands target the SteelSeries Windows driver (OS-level
		// CPI, acceleration, firmware flashing). They have no Linux
		// counterpart; sleeps are honoured so timing stays the same.
		if c.Type == sseCommandSleep && len(data) >= 4 {
			time.Sleep(time.Duration(binary.LittleEndian.Uint32(data)) * time.Millisecond)
		}
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("protocol %d is not supported on Linux", proto)
}

func (d *Decl) newIOState() *ioState {
	tb := defaultTimeBetweenCmd
	if v, err := d.Call("time-between-commands"); err == nil && golisp.IntegerP(v) {
		tb = time.Duration(golisp.IntegerValue(v)) * time.Millisecond
	}
	to := 500 * time.Millisecond
	if v, err := d.Call("max-usb-timeout"); err == nil && golisp.IntegerP(v) {
		to = time.Duration(golisp.IntegerValue(v)) * time.Millisecond
	}
	return &ioState{timeBetween: tb, timeout: to}
}

// WriteApi serializes values through the api's struct and sends it.
func (d *Decl) WriteApi(name string, values any) error {
	api := d.Api(name)
	if api == nil {
		return fmt.Errorf("device %s has no api %q", d.Name, name)
	}
	if api.IsFirmwareUpdate && !d.rt.AllowFirmware {
		return ErrFirmwareDisabled
	}
	d.mu.Lock()
	blocked := d.blocked && !api.Unblockable
	d.mu.Unlock()
	if blocked {
		return fmt.Errorf("commands blocked for %s", d.Name)
	}
	if len(api.Write) == 0 {
		return fmt.Errorf("api %s is not writable", name)
	}
	structName := name
	if api.derivedFrom != "" && d.Struct(name) == nil {
		structName = api.derivedFrom
	}
	var payload []byte
	var payloadObj *golisp.Data
	m, isMap := values.(map[string]any)
	if st := d.Struct(structName); st != nil {
		merged := st.Defaults(d.rt)
		for k, v := range d.CachedValues(name) {
			merged[k] = v
		}
		for k, v := range m {
			merged[k] = v
		}
		b, err := st.Encode(d.rt, st.Outgoing, merged)
		if err != nil {
			return err
		}
		payload, payloadObj, m = b, bytesObj(b), merged
	} else if !isMap {
		payloadObj = toLisp(values)
	} else {
		payloadObj = bytesObj(nil)
	}
	env, err := d.apiEnv(api, payloadObj)
	if err != nil {
		return err
	}
	if isMap && !api.CacheExempt {
		d.setWanted(name, m)
	}
	st := d.newIOState()
	var last []byte
	var runErr error
	for _, cmd := range api.Write {
		res, err := d.runCommand(cmd, env, st, payload, func(c *Chunk) bool { return api.MixesReadsWrites && c.SetReadBit })
		for _, r := range res {
			if r.data != nil {
				last = r.data
			}
		}
		if err != nil {
			runErr = err
			break
		}
	}
	if runErr == nil && api.ReadbackErrByte != nil && len(last) > int(api.ReadbackErrByte[0]) && int64(last[api.ReadbackErrByte[0]]) == api.ReadbackErrByte[1] {
		runErr = fmt.Errorf("device reported an error writing %s", name)
	}
	env.BindLocallyTo(golisp.Intern("readback-payload"), bytesObj(last))
	if runErr != nil {
		if api.OnWriteFailure != nil {
			d.rt.evalBody(api.OnWriteFailure, env)
		}
	} else {
		if isMap && !api.CacheExempt {
			d.setCache(name, m)
		}
		if api.OnWriteSuccess != nil {
			if _, err := d.rt.evalBody(api.OnWriteSuccess, env); err != nil {
				slog.Debug("api-on-write-success", "api", name, "err", err)
			}
		}
	}
	if api.Finally != nil {
		d.rt.evalBody(api.Finally, env)
	}
	if api.HoldAfterWriteMs > 0 {
		time.Sleep(time.Duration(api.HoldAfterWriteMs) * time.Millisecond)
	}
	return runErr
}

// ReadApi performs the api's read chunks and decodes the reply.
func (d *Decl) ReadApi(name string, args map[string]any) (map[string]any, error) {
	api := d.Api(name)
	if api == nil {
		return nil, fmt.Errorf("device %s has no api %q", d.Name, name)
	}
	if api.Read == nil {
		return nil, fmt.Errorf("api %s is not readable", name)
	}
	structName := name
	if api.derivedFrom != "" && d.Struct(name) == nil {
		structName = api.derivedFrom
	}
	st := d.Struct(structName)
	var payload []byte
	if st != nil {
		vals := st.Defaults(d.rt)
		for k, v := range args {
			vals[k] = v
		}
		b, err := st.Encode(d.rt, st.Outgoing, vals)
		if err != nil {
			return nil, err
		}
		payload = b
	}
	env, err := d.apiEnv(api, bytesObj(payload))
	if err != nil {
		return nil, err
	}
	io := d.newIOState()
	res, runErr := d.runCommand(api.Read, env, io, payload, func(*Chunk) bool { return true })
	var data []byte
	if runErr == nil {
		data, runErr = d.assembleRead(api, env, res)
	}
	env.BindLocallyTo(golisp.Intern("payload"), bytesObj(data))
	if runErr != nil {
		if api.OnReadFailure != nil {
			d.rt.evalBody(api.OnReadFailure, env)
		}
		if api.Finally != nil {
			d.rt.evalBody(api.Finally, env)
		}
		return nil, runErr
	}
	out := map[string]any{}
	if st != nil {
		out, _, err = st.Decode(d.rt, st.Incoming, data)
		if err != nil {
			return nil, err
		}
	}
	if !api.CacheExempt {
		d.setCache(name, out)
	}
	if api.OnReadSuccess != nil {
		if _, err := d.rt.evalBody(api.OnReadSuccess, env); err != nil {
			slog.Debug("api-on-read-success", "api", name, "err", err)
		}
	}
	if api.Finally != nil {
		d.rt.evalBody(api.Finally, env)
	}
	return out, nil
}

// assembleRead combines chunk replies into the buffer the incoming struct
// is decoded from, honouring (read-chunks-byte-specifiers ...).
func (d *Decl) assembleRead(api *Api, env *golisp.SymbolTableFrame, res []chunkResult) ([]byte, error) {
	if api.ReadChunksSpecFn != nil {
		v, err := d.rt.eval(api.ReadChunksSpecFn, env)
		if err != nil {
			return nil, err
		}
		specs := flattenSpecifiers(v)
		var out []byte
		for i, r := range res {
			if i >= len(specs) {
				break
			}
			s := specs[i]
			if s.start < len(r.data) {
				end := s.start + s.n
				if end > len(r.data) {
					end = len(r.data)
				}
				out = append(out, r.data[s.start:end]...)
			}
			out = append(out, make([]byte, s.zeros)...)
		}
		return out, nil
	}
	var reads [][]byte
	for _, r := range res {
		if r.isRead && r.data != nil {
			reads = append(reads, r.data)
		}
	}
	switch len(reads) {
	case 0:
		return nil, nil
	case 1:
		return reads[0], nil
	}
	var out []byte
	for _, r := range reads {
		out = append(out, r...)
	}
	return out, nil
}

type readSpecifier struct{ start, n, zeros int }

func flattenSpecifiers(v *golisp.Data) []readSpecifier {
	var out []readSpecifier
	for _, e := range golisp.ToArray(v) {
		parts := golisp.ToArray(e)
		if len(parts) == 0 {
			continue
		}
		if golisp.SymbolP(parts[0]) && golisp.StringValue(parts[0]) == "repeat" && len(parts) >= 3 {
			n := int(golisp.IntegerValue(parts[1]))
			inner := flattenSpecifiers(golisp.ArrayToList(parts[2:]))
			for i := 0; i < n; i++ {
				out = append(out, inner...)
			}
			continue
		}
		s := readSpecifier{}
		if len(parts) > 0 {
			s.start = int(golisp.IntegerValue(parts[0]))
		}
		if len(parts) > 1 {
			s.n = int(golisp.IntegerValue(parts[1]))
		}
		if len(parts) > 2 {
			s.zeros = int(golisp.IntegerValue(parts[2]))
		}
		out = append(out, s)
	}
	return out
}
