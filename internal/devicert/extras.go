package devicert

import (
	"strconv"
	"strings"

	"github.com/steelseries/golisp"
)

// semverInRange implements the blang/semver range syntax the specs use:
// space-separated comparisons are ANDed, "||" separates alternatives.
// Versions may have build suffixes ("1.0.22 +1922679c") or fewer than
// three components ("2.31").
func semverInRange(version, rng string) bool {
	v, ok := parseVersion(version)
	if !ok {
		return false
	}
	for _, alt := range strings.Split(rng, "||") {
		all := true
		for _, cond := range strings.Fields(alt) {
			op := strings.TrimRight(cond, "0123456789.xX*")
			want, ok := parseVersion(strings.TrimPrefix(cond, op))
			if !ok {
				all = false
				break
			}
			c := compareVersion(v, want)
			var pass bool
			switch op {
			case ">=":
				pass = c >= 0
			case ">":
				pass = c > 0
			case "<=":
				pass = c <= 0
			case "<":
				pass = c < 0
			case "!=", "!":
				pass = c != 0
			default:
				pass = c == 0
			}
			if !pass {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " +-"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return v, false
	}
	for i, p := range strings.SplitN(s, ".", 3) {
		if p == "x" || p == "X" || p == "*" {
			break
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func compareVersion(a, b [3]int) int {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// ---------------------------------------------------------------- sequences

// Lighting sequences: (template NAME struct-body...) declares the frame
// struct a sequence writes, and (sequence API TEMPLATE NAME (api-write ...))
// declares an api whose settings are colour animations. The Windows
// sequencer plays those animations in native code; here a sequence write
// sends its first frame, which covers static colours.
func registerSequenceForms() {
	sf("template", "*", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
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
	sf("sequence", "*", func(a []*golisp.Data, env *golisp.SymbolTableFrame) (*golisp.Data, error) {
		d, err := currentDecl(env)
		if err != nil {
			return nil, err
		}
		if len(a) < 3 {
			return nil, nil
		}
		apiName, tmpl, seqName := sym(a[0]), sym(a[1]), sym(a[2])
		api, err := buildApi(d, apiName, a[3:], env, nil)
		if err != nil {
			return nil, err
		}
		api.SequenceTemplate, api.SequenceName = tmpl, seqName
		d.mu.Lock()
		d.Apis[apiName] = api
		if st, ok := d.Structs[tmpl]; ok {
			if _, exists := d.Structs[apiName]; !exists {
				d.Structs[apiName] = st
			}
		}
		d.sequences[apiName] = &Sequence{Api: apiName, Template: tmpl, Name: seqName}
		d.mu.Unlock()
		return nil, nil
	})
	sf("rewrite-instructions", "3", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	sf("rewrite-instructions-range", "4", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	sf("force-rewrite-apis", ">=2", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
}

// ------------------------------------------------------------ game events

// GameSense handler builtins. Game event handling runs in the engine's
// GameSense service; at spec-load time these only need to exist.
func registerEventBuiltins() {
	names := []string{"handle-event", "load-stored-game-events", "load-in-game-sandbox", "eval-in-game-sandbox", "game-sandbox",
		"current-env-if-inside-game-sandbox", "load-hax0r-bindings", "load-custom-game-bindings", "unhandled-device-ids", "handle-device-id",
		"event-autoinit-exclusion", "event-run-when-disabled", "define-custom-zones", "add-custom-zone", "get-custom-zones", "cleanup-custom-zones",
		"add-event-per-key-zone-use", "add-event-zone-use-at-level-with-specifier-and-type", "add-event-zone-use-with-specifier",
		"add-event-zone-use-with-specifier-and-type", "clear-event-per-key-zones-at-level", "get-composite-zones-in-use", "get-per-key-zones-in-use",
		"all-events-use-this-per-key-zone", "deinitialize-timer-length", "get-deinitialize-timer-length", "no-reinitialize", "is-reinitialize-allowed",
		"ti-predefined-function-value-from-name", "get-connected-rgb-per-key-zone-device-pids", "get-connected-rgb-composite-zone-device-pids",
		"connected-unlocked-gamesense-device-ids", "is-rgb-zoned-device-specifier", "applicable-rgb-zoned-device-specifiers",
		"expand-color-and-zone-list-to-rgb-zoned-devices", "get-lighting-zones-in-use-by-event-list-and-specifier-info", "get-cached-merged-lighting",
		"cache-merged-lighting", "is-merged-lighting-enabled", "get-event-from-selector", "get-lighting-zones-in-use-for-device",
		"get-screen-zones-in-use-for-device", "get-tactile-zones-in-use-for-device", "disable-unusable-lighting-zone", "notify-gamesense-init-changed",
		"handle-autoclip", "get-mapped-pids-for-main-pid"}
	for _, n := range names {
		pf(n, "*", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	}
	for _, n := range []string{"handler", "handler-with-level", "handler-with-post-event"} {
		sf(n, "*", func([]*golisp.Data, *golisp.SymbolTableFrame) (*golisp.Data, error) { return nil, nil })
	}
}
