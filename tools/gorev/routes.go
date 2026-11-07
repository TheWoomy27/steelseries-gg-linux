package main

import (
	"debug/gosym"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/arch/x86/x86asm"
)

// event is one interesting thing seen while walking a function's code:
// a string literal, a function reference, or a call.
type event struct {
	kind string // "str", "ref", "call"
	val  string
}

// walk decodes fn and reports string literals (LEA + length immediate in
// either order), referenced functions and direct calls, in code order.
func (b *bin) walk(fn *gosym.Func) []event {
	var out []event
	code := b.read(fn.Entry, int(fn.End-fn.Entry))
	pc := fn.Entry
	var leaAddr uint64
	var lastImm int64
	for len(code) > 0 {
		inst, err := x86asm.Decode(code, 64)
		if err != nil || inst.Len == 0 {
			code, pc = code[1:], pc+1
			continue
		}
		next := pc + uint64(inst.Len)
		switch inst.Op {
		case x86asm.LEA:
			if m, ok := inst.Args[1].(x86asm.Mem); ok && m.Base == x86asm.RIP {
				tgt := uint64(int64(next) + m.Disp)
				if f := b.byEntry[tgt]; f != nil {
					out = append(out, event{"ref", f.Name})
				} else if pv := b.u64(tgt); b.byEntry[pv] != nil {
					out = append(out, event{"ref", b.byEntry[pv].Name})
				} else {
					leaAddr = tgt
					// length loaded before the pointer (mov ebx, N; lea rax, s)
					if lastImm > 0 && lastImm < 512 {
						if s := b.read(tgt, int(lastImm)); len(s) == int(lastImm) && printableRun(s) == string(s) {
							out = append(out, event{"str", string(s)})
							leaAddr = 0
						}
					}
				}
			}
			lastImm = 0
		case x86asm.MOV:
			if v, ok := inst.Args[1].(x86asm.Imm); ok {
				if leaAddr != 0 && v > 0 && v < 512 {
					if s := b.read(leaAddr, int(v)); len(s) == int(v) && printableRun(s) == string(s) {
						out = append(out, event{"str", string(s)})
					}
					leaAddr = 0
				} else {
					lastImm = int64(v)
				}
			}
		case x86asm.CALL:
			if r, ok := inst.Args[0].(x86asm.Rel); ok {
				if f := b.table.PCToFunc(uint64(int64(next) + int64(r))); f != nil {
					out = append(out, event{"call", f.Name})
				}
			}
		}
		code, pc = code[inst.Len:], next
	}
	return out
}

var methods = map[string]bool{"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true}

// routes prints HTTP routes registered by functions matching re, for both
// gorilla/mux (HandleFunc(path).Methods(m)) and steelseries/web
// (addRoute(path, method, handler)).
func (b *bin) routes(re *regexp.Regexp) {
	for i := range b.table.Funcs {
		fn := &b.table.Funcs[i]
		if !re.MatchString(fn.Name) {
			continue
		}
		var path, method, handler string
		var strs []string
		for _, ev := range b.walk(fn) {
			switch ev.kind {
			case "str":
				strs = append(strs, ev.val)
				if strings.HasPrefix(ev.val, "/") {
					path = ev.val
				}
				if methods[ev.val] {
					method = ev.val
				}
			case "ref":
				if !strings.Contains(ev.val, ".Routes.") || handler == "" {
					handler = ev.val
				}
			case "call":
				switch {
				case strings.HasSuffix(ev.val, "web.(*Server).addRoute"):
					fmt.Printf("%-7s %-60s %s\n", method, path, short(handler))
					path, method, handler = "", "", ""
				case strings.HasSuffix(ev.val, "mux.(*Route).Methods"):
					m := method
					if m == "" && len(strs) > 0 {
						m = strs[len(strs)-1]
					}
					fmt.Printf("%-7s %-60s %s\n", m, path, short(handler))
					path, method, handler = "", "", ""
				case strings.HasSuffix(ev.val, "mux.(*Router).PathPrefix"):
					fmt.Printf("PREFIX  %s\n", path)
				}
			}
		}
	}
}

func short(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// summary prints, per matching function, its string literals and callees,
// which is usually enough to see a handler's response keys.
func (b *bin) summary(re *regexp.Regexp) {
	skip := regexp.MustCompile(`^(runtime|sync|fmt|strconv|errors|internal|reflect|encoding/json|github.com/steelseries/log)`)
	var names []string
	byName := map[string][]event{}
	for i := range b.table.Funcs {
		fn := &b.table.Funcs[i]
		if re.MatchString(fn.Name) {
			names = append(names, fn.Name)
			byName[fn.Name] = b.walk(fn)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		var parts []string
		seen := map[string]bool{}
		for _, ev := range byName[n] {
			v := ev.val
			if ev.kind == "str" {
				v = fmt.Sprintf("%q", v)
			} else if ev.kind == "call" && !skip.MatchString(v) && v != n {
				v = "->" + short(v)
			} else {
				continue
			}
			if !seen[v] {
				seen[v] = true
				parts = append(parts, v)
			}
		}
		if len(parts) > 0 {
			fmt.Printf("%s\n    %s\n", short(n), strings.Join(parts, " "))
		}
	}
}
