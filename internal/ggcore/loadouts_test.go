package ggcore

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ggl/internal/httpx"
)

// The client deploys a profile at /v1/loadouts/{id}/deploy; its Prism
// configuration goes to the loadoutChanged hook (Prism), not the Engine.
func TestDeployLoadoutRoute(t *testing.T) {
	c := testCore(t)
	c.srv = httpx.NewServer("gg", "")
	c.loadoutRoutes()
	var id string
	c.withLoadouts(func(st *loadoutState) (any, int) {
		st.Loadouts = append(st.Loadouts, Loadout{ID: "p1", Name: "Profile", Configs: []LoadoutConfig{{ConfigType: "prism", ConfigID: "cfg-1"}}})
		id = "p1"
		return nil, 200
	})
	var got []LoadoutConfig
	c.OnLoadoutChange(func(cfgs []LoadoutConfig) { got = cfgs })
	for _, path := range []string{"/v1/loadouts/" + id + "/deploy", "/v1/loadouts/missing/deploy"} {
		w := httptest.NewRecorder()
		c.srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
		want := http.StatusOK
		if path != "/v1/loadouts/"+id+"/deploy" {
			want = http.StatusNotFound
		}
		if w.Code != want {
			t.Errorf("POST %s: %d, want %d", path, w.Code, want)
		}
	}
	if len(got) != 1 || got[0].ConfigType != "prism" || got[0].ConfigID != "cfg-1" {
		t.Errorf("hook got %+v", got)
	}
	if st := c.loadLoadouts(); st.ActiveID != id {
		t.Errorf("active profile %q", st.ActiveID)
	}
	w := httptest.NewRecorder()
	c.srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/loadouts/profiles/enable", nil))
	if st := c.loadLoadouts(); w.Code != http.StatusOK || !st.ProfilesEnabled {
		t.Errorf("profiles/enable: %d, enabled %v", w.Code, st.ProfilesEnabled)
	}
}
