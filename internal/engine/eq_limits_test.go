package engine

import (
	"net/http/httptest"
	"strings"
	"testing"

	"ggl/internal/httpx"
)

// GG 124's device EQ page reads the headset's limits; a device without
// any gets null, as from the Windows Engine.
func TestEQLimits(t *testing.T) {
	e := &Engine{srv: httpx.NewServer("engine", "")}
	e.eqLimitRoutes()
	get := func(path string) string {
		w := httptest.NewRecorder()
		e.srv.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return strings.TrimSpace(w.Body.String())
	}
	for path, want := range map[string]string{
		// Arctis Nova 7 Gen 2: a filter that is off goes as Q 1.414, type 0, gain 0.
		"/parametricEQ/358/deviceEQLimitation": `{"deviceEQLimitation":{"maxGain":12,"minGain":-12,"disabledFrequency":20001,"disabledQFactor":1.414,"disabledFilterType":0,"disabledGains":0,"unsupportedEq":null}}`,
		"/parametricEQ/308/deviceEQLimitation": `{"deviceEQLimitation":{"maxGain":10,"minGain":-10,"disabledFrequency":20001,"disabledQFactor":null,"disabledFilterType":null,"disabledGains":null,"unsupportedEq":null}}`,
		"/parametricEQ/1/deviceEQLimitation":   `{"deviceEQLimitation":null}`,
	} {
		if got := get(path); got != want {
			t.Errorf("%s:\n got %s\nwant %s", path, got, want)
		}
	}
	if got := get("/parametricEQ/316/deviceEQLimitation"); !strings.Contains(got, `"unsupportedEq":["4cb0fad9-34b6-4a3a-9a8f-0e210cfd57a1","726cc928-eb01-4d8f-b0e9-d594eb19fc37"]`) {
		t.Errorf("Nova Elite: %s", got)
	}
}
