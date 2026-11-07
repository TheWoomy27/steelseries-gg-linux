package ggcore

import (
	"log/slog"
	"net/http"

	"ggl/internal/httpx"
)

// SubApp mirrors sub_apps.SubApp from the GG core.
type SubApp struct {
	Name                   string               `json:"name"`
	IsEnabled              bool                 `json:"isEnabled"`
	IsReady                bool                 `json:"isReady"`
	IsRunning              bool                 `json:"isRunning"`
	ExitCode               int                  `json:"exitCode"`
	ShouldAutoStart        bool                 `json:"shouldAutoStart"`
	IsWindowsSupported     bool                 `json:"isWindowsSupported"`
	IsMacSupported         bool                 `json:"isMacSupported"`
	ToggleViaSettings      bool                 `json:"toggleViaSettings"`
	IsBrowserViewSupported bool                 `json:"isBrowserViewSupported"`
	Metadata               SubAppMetadata       `json:"metadata"`
	SecretMetadata         SubAppSecretMetadata `json:"secretMetadata"`
}

type SubAppMetadata struct {
	EncryptedWebServerAddress string `json:"encryptedWebServerAddress"`
	WebServerAddress          string `json:"webServerAddress"`
	OfflineFrontendAddress    string `json:"offlineFrontendAddress"`
	OnlineFrontendAddress     string `json:"onlineFrontendAddress"`
}

type SubAppSecretMetadata struct {
	EncryptedWebServerAddressCertText string `json:"encryptedWebServerAddressCertText"`
}

// Event payload for EVENT_SUB_APP_* (sub_apps events carry the app name
// as subAppName together with the metadata).
type subAppEvent struct {
	SubAppName string         `json:"subAppName"`
	Metadata   SubAppMetadata `json:"metadata"`
	IsEnabled  bool           `json:"isEnabled"`
}

// Sub-apps known to GG. On Linux every one of them is provided by ggd
// itself except threeDAT (a Windows 3D audio tool), which stays
// unsupported.
var subAppDefaults = []SubApp{
	{Name: "engine", IsEnabled: true, ShouldAutoStart: true, IsWindowsSupported: true, IsMacSupported: true},
	{Name: "prismSyncV2", IsEnabled: true, ShouldAutoStart: true, IsWindowsSupported: true, IsMacSupported: true},
	{Name: "sonar", IsEnabled: false, IsWindowsSupported: true, IsMacSupported: true, ToggleViaSettings: true},
	{Name: "moments", IsEnabled: true, ShouldAutoStart: true, IsWindowsSupported: true, IsMacSupported: true},
	{Name: "threeDAT", IsEnabled: false, IsWindowsSupported: false, IsMacSupported: false},
}

func (c *Core) initSubApps() {
	enabled := map[string]bool{}
	c.db.Get("subAppsEnabled", &enabled)
	c.subApps = map[string]*SubApp{}
	for _, d := range subAppDefaults {
		a := d
		if v, ok := enabled[a.Name]; ok && a.IsWindowsSupported {
			a.IsEnabled = v
		}
		c.subApps[a.Name] = &a
	}
}

// subAppReady marks a sub-app running at the given addresses and notifies
// the client, which uses the address to reach that service.
func (c *Core) subAppReady(name string, md SubAppMetadata) {
	c.mu.Lock()
	a, ok := c.subApps[name]
	if !ok {
		c.mu.Unlock()
		return
	}
	a.Metadata = md
	a.IsRunning, a.IsReady = true, true
	ev := subAppEvent{SubAppName: name, Metadata: md, IsEnabled: a.IsEnabled}
	c.mu.Unlock()
	c.Broadcast("EVENT_SUB_APP_STARTING", ev)
	c.Broadcast("EVENT_SUB_APP_READY", ev)
	c.eng.SubAppEvent("EVENT_ENGINE_SUB_APP_READY", ev)
}

// RegisterSubApp is used by in-process services (Sonar, Moments...) once
// their servers are listening.
func (c *Core) RegisterSubApp(name string, md SubAppMetadata) {
	c.mu.Lock()
	enabled := c.subApps[name] != nil && c.subApps[name].IsEnabled
	c.mu.Unlock()
	if enabled {
		c.subAppReady(name, md)
	}
}

// SubApps returns a snapshot of every sub-app, as GET /subApps reports it.
func (c *Core) SubApps() map[string]SubApp {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]SubApp{}
	for k, v := range c.subApps {
		out[k] = *v
	}
	return out
}

// replaySubApps tells a newly connected client about running sub-apps;
// it would otherwise only learn addresses from events sent before it
// connected.
func (c *Core) replaySubApps(send func(event string, data any)) {
	for name, a := range c.SubApps() {
		if a.IsRunning {
			ev := subAppEvent{SubAppName: name, Metadata: a.Metadata, IsEnabled: a.IsEnabled}
			send("EVENT_SUB_APP_CREATED", ev)
			send("EVENT_SUB_APP_READY", ev)
		}
	}
}

func (c *Core) subAppRoutes() {
	s := c.srv
	s.Handle("GET /subApps", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"subApps": c.SubApps()})
	})
	s.Handle("POST /subApps/status", func(w http.ResponseWriter, r *http.Request) {
		p, err := decode[struct {
			Name      string   `json:"name"`
			IsEnabled bool     `json:"isEnabled"`
			Args      []string `json:"additionalArguments"`
		}](r)
		if err != nil {
			httpx.Error(w, 400, err.Error())
			return
		}
		c.setSubAppEnabled(p.Name, p.IsEnabled)
		httpx.OK(w)
	})
	s.Handle("POST /subApps/ready", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
	s.Handle("POST /subApps/register", func(w http.ResponseWriter, r *http.Request) {
		p, err := decode[struct {
			Name     string         `json:"name"`
			Metadata SubAppMetadata `json:"metadata"`
		}](r)
		if err != nil {
			httpx.Error(w, 400, err.Error())
			return
		}
		c.RegisterSubApp(p.Name, p.Metadata)
		httpx.OK(w)
	})
	s.Handle("POST /subApps/{id}/unlockFeature", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w) })
}

func (c *Core) setSubAppEnabled(name string, on bool) {
	c.mu.Lock()
	a, ok := c.subApps[name]
	if !ok || !a.IsWindowsSupported {
		c.mu.Unlock()
		slog.Warn("sub-app not available on linux", "name", name)
		return
	}
	a.IsEnabled = on
	enabled := map[string]bool{}
	for k, v := range c.subApps {
		enabled[k] = v.IsEnabled
	}
	md := a.Metadata
	c.mu.Unlock()
	c.db.Set("subAppsEnabled", enabled)

	ev := subAppEvent{SubAppName: name, Metadata: md, IsEnabled: on}
	if on {
		c.Broadcast("EVENT_SUB_APP_CREATED", ev)
		if hook := c.subAppHooks[name]; hook != nil {
			hook(true)
		}
	} else {
		if hook := c.subAppHooks[name]; hook != nil {
			hook(false)
		}
		c.mu.Lock()
		a.IsRunning, a.IsReady = false, false
		c.mu.Unlock()
		c.Broadcast("EVENT_SUB_APP_DISABLE", ev)
		c.Broadcast("EVENT_SUB_APP_SHUTDOWN", ev)
	}
}
