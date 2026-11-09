package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ggl/internal/paths"
)

// ggdClient talks to the running ggd's local HTTPS services the way the
// GG client does: with the session token and ggd's own certificate.
type ggdClient struct {
	http  *http.Client
	token string
	gg    string // the GG core's address
}

func newGGDClient() (*ggdClient, error) {
	b, err := os.ReadFile(paths.CoreProps())
	if err != nil {
		return nil, fmt.Errorf("ggd is not running (%v)", err)
	}
	var props struct {
		GG string `json:"ggEncryptedAddress"`
	}
	if err := json.Unmarshal(b, &props); err != nil || props.GG == "" {
		return nil, fmt.Errorf("unreadable %s", paths.CoreProps())
	}
	token, err := os.ReadFile(filepath.Join(paths.State(), "client-token"))
	if err != nil {
		return nil, err
	}
	pem, err := os.ReadFile(filepath.Join(paths.TLSDir(), "cert.pem"))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	// ggd's certificate is for localhost, not the loopback address.
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"}}
	return &ggdClient{http: &http.Client{Transport: tr, Timeout: 3 * time.Minute}, token: strings.TrimSpace(string(token)), gg: props.GG}, nil
}

func (c *ggdClient) do(method, url string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(b))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// subAppAddress is a running sub-app's HTTPS address.
func (c *ggdClient) subAppAddress(name string) (string, error) {
	var r struct {
		SubApps map[string]struct {
			IsEnabled bool `json:"isEnabled"`
			IsRunning bool `json:"isRunning"`
			Metadata  struct {
				Encrypted string `json:"encryptedWebServerAddress"`
			} `json:"metadata"`
		} `json:"subApps"`
	}
	if err := c.do("GET", "https://"+c.gg+"/subApps", nil, &r); err != nil {
		return "", err
	}
	a, ok := r.SubApps[name]
	if !ok || !a.IsEnabled || !a.IsRunning || a.Metadata.Encrypted == "" {
		return "", fmt.Errorf("%s is not running (switch it on in GG's settings)", name)
	}
	return a.Metadata.Encrypted, nil
}

// momentsCommand runs "ggctl moments ...".
func momentsCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: ggctl moments clip | toggle | mic | desktop | status")
	}
	c, err := newGGDClient()
	if err != nil {
		return err
	}
	addr, err := c.subAppAddress("moments")
	if err != nil {
		return err
	}
	switch args[0] {
	case "clip":
		var r struct {
			Clip struct {
				Path string `json:"path"`
			} `json:"clip"`
		}
		if err := c.do("POST", "https://"+addr+"/moments/clip", nil, &r); err != nil {
			return err
		}
		fmt.Println(r.Clip.Path)
	case "toggle", "mic", "desktop":
		what := map[string]string{"toggle": "capture", "mic": "mic", "desktop": "desktop"}[args[0]]
		var r struct {
			On bool `json:"on"`
		}
		if err := c.do("POST", "https://"+addr+"/moments/toggle/"+what, nil, &r); err != nil {
			return err
		}
		if what == "mic" {
			fmt.Println(map[bool]string{true: "mic muted", false: "mic unmuted"}[r.On])
		} else {
			fmt.Println(what, map[bool]string{true: "on", false: "off"}[r.On])
		}
	case "status":
		var r struct {
			Status struct {
				Status string `json:"status"`
				Game   struct {
					Name string `json:"name"`
				} `json:"game"`
				Desktop bool `json:"desktop_mode_running"`
			} `json:"MomentsStatus"`
		}
		if err := c.do("GET", "https://"+addr+"/moments/status", nil, &r); err != nil {
			return err
		}
		what := r.Status.Game.Name
		if r.Status.Desktop {
			what = "desktop"
		}
		fmt.Println(strings.TrimSpace(r.Status.Status + " " + what))
	default:
		return fmt.Errorf("unknown moments command %q", args[0])
	}
	return nil
}
