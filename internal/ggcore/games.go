package ggcore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"ggl/internal/httpx"
)

// Scanner ids from the client's ScannerType enum. Manual entries use 0.
const (
	scannerManual = 0
	scannerSteam  = 1
	scannerGOG    = 3
	scannerEpic   = 9
)

// ScannedGame mirrors publicapi.ScannedGamesPayloadV1.
type ScannedGame struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Directory           string   `json:"directory"`
	ExePath             string   `json:"exe_path"`
	LauncherExecutables []string `json:"launcher_executables"`
	UniqueIdentifier    string   `json:"unique_identifier"`
	FeaturedGameID      int      `json:"featured_game_id"`
	ImagePath           string   `json:"image_path"`
	SpecialIconCSS      string   `json:"special_icon_css"`
	ScannerID           int      `json:"scanner_id"`
	Uninstalled         bool     `json:"uninstalled"`
	Tags                []string `json:"tags"`
	IsFavorite          bool     `json:"isFavorite"`
	steamAppID          string
}

type gameLibrary struct {
	mu    sync.Mutex
	games []ScannedGame
	when  time.Time
}

func home() string { h, _ := os.UserHomeDir(); return h }

var vdfPath = regexp.MustCompile(`"path"\s+"([^"]+)"`)
var acfField = regexp.MustCompile(`"(appid|name|installdir)"\s+"([^"]*)"`)

// Steam tools that show up as apps but are not games.
var steamTools = regexp.MustCompile(`(?i)^(proton|steam linux runtime|steamworks common|steamvr)`)

func scanSteam() []ScannedGame {
	roots := []string{
		filepath.Join(home(), ".local/share/Steam"),
		filepath.Join(home(), ".steam/steam"),
		filepath.Join(home(), ".var/app/com.valvesoftware.Steam/data/Steam"),
	}
	libs := map[string]bool{}
	for _, r := range roots {
		b, err := os.ReadFile(filepath.Join(r, "steamapps", "libraryfolders.vdf"))
		if err != nil {
			continue
		}
		libs[filepath.Clean(r)] = true
		for _, m := range vdfPath.FindAllStringSubmatch(string(b), -1) {
			libs[filepath.Clean(strings.ReplaceAll(m[1], `\\`, `/`))] = true
		}
	}
	seen := map[string]bool{}
	var out []ScannedGame
	for lib := range libs {
		manifests, _ := filepath.Glob(filepath.Join(lib, "steamapps", "appmanifest_*.acf"))
		for _, mf := range manifests {
			b, err := os.ReadFile(mf)
			if err != nil {
				continue
			}
			f := map[string]string{}
			for _, m := range acfField.FindAllStringSubmatch(string(b), -1) {
				if _, ok := f[m[1]]; !ok {
					f[m[1]] = m[2]
				}
			}
			if f["appid"] == "" || seen[f["appid"]] || steamTools.MatchString(f["name"]) {
				continue
			}
			seen[f["appid"]] = true
			dir := filepath.Join(lib, "steamapps", "common", f["installdir"])
			img := filepath.Join(lib, "appcache", "librarycache", f["appid"]+"_library_600x900.jpg")
			if _, err := os.Stat(img); err != nil {
				img = ""
			}
			out = append(out, ScannedGame{
				ID: "steam-" + f["appid"], Name: f["name"], Directory: dir, UniqueIdentifier: "steam_" + f["appid"],
				ImagePath: img, ScannerID: scannerSteam, steamAppID: f["appid"],
			})
		}
	}
	return out
}

// scanHeroic reads Heroic's Epic (legendary) and GOG install records.
func scanHeroic() []ScannedGame {
	var out []ScannedGame
	base := filepath.Join(home(), ".config/heroic")
	if b, err := os.ReadFile(filepath.Join(base, "legendaryConfig/legendary/installed.json")); err == nil {
		var m map[string]struct {
			AppName     string `json:"app_name"`
			Title       string `json:"title"`
			InstallPath string `json:"install_path"`
			Executable  string `json:"executable"`
		}
		if json.Unmarshal(b, &m) == nil {
			for _, g := range m {
				out = append(out, ScannedGame{ID: "epic-" + g.AppName, Name: g.Title, Directory: g.InstallPath,
					ExePath: filepath.Join(g.InstallPath, g.Executable), UniqueIdentifier: "epic_" + g.AppName, ScannerID: scannerEpic})
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(base, "gog_store/installed.json")); err == nil {
		var m struct {
			Installed []struct {
				AppName     string `json:"appName"`
				InstallPath string `json:"install_path"`
			} `json:"installed"`
		}
		if json.Unmarshal(b, &m) == nil {
			for _, g := range m.Installed {
				out = append(out, ScannedGame{ID: "gog-" + g.AppName, Name: filepath.Base(g.InstallPath), Directory: g.InstallPath,
					UniqueIdentifier: "gog_" + g.AppName, ScannerID: scannerGOG})
			}
		}
	}
	return out
}

// scanLutris reads Lutris' game database.
func scanLutris() []ScannedGame {
	path := filepath.Join(home(), ".local/share/lutris/pga.db")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query(`SELECT slug, name, coalesce(directory,'') FROM games WHERE installed = 1`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ScannedGame
	for rows.Next() {
		var slug, name, dir string
		if rows.Scan(&slug, &name, &dir) == nil {
			out = append(out, ScannedGame{ID: "lutris-" + slug, Name: name, Directory: dir, UniqueIdentifier: "lutris_" + slug, ScannerID: scannerManual})
		}
	}
	return out
}

func (c *Core) scanGames() []ScannedGame {
	var games []ScannedGame
	games = append(games, scanSteam()...)
	games = append(games, scanHeroic()...)
	games = append(games, scanLutris()...)
	var manual []ScannedGame
	c.db.Get("manualGames", &manual)
	games = append(games, manual...)
	removed := map[string]bool{}
	c.db.Get("removedGames", &removed)
	tags := map[string][]string{}
	c.db.Get("gameTags", &tags)
	out := games[:0]
	for _, g := range games {
		if removed[g.ID] {
			continue
		}
		g.Tags = tags[g.ID]
		if g.Tags == nil {
			g.Tags = []string{}
		}
		for _, t := range g.Tags {
			if t == "favorite" {
				g.IsFavorite = true
			}
		}
		if g.LauncherExecutables == nil {
			g.LauncherExecutables = []string{}
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	c.games.mu.Lock()
	c.games.games, c.games.when = out, time.Now()
	c.games.mu.Unlock()
	return out
}

func (c *Core) cachedGames() []ScannedGame {
	c.games.mu.Lock()
	g, when := c.games.games, c.games.when
	c.games.mu.Unlock()
	if when.IsZero() || time.Since(when) > 5*time.Minute {
		return c.scanGames()
	}
	return g
}

func (c *Core) gameRoutes() {
	s := c.srv
	s.Handle("GET /v1/scannedGames", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"scannedGames": c.cachedGames()})
	})
	s.Handle("POST /v1/scanForApplications", func(w http.ResponseWriter, r *http.Request) {
		games := c.scanGames()
		c.Broadcast("EVENT_APPLICATIONS_SCANNED", map[string]any{"count": len(games)})
		httpx.OK(w)
	})
	s.Handle("GET /v1/applicationScannerInfo", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"scanners": []any{}})
	})
	s.Handle("PUT /v1/scannedGames/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			Tags []string `json:"tags"`
		}](r)
		tags := map[string][]string{}
		c.db.Get("gameTags", &tags)
		tags[r.PathValue("id")] = p.Tags
		c.db.Set("gameTags", tags)
		c.scanGames()
		httpx.OK(w)
	})
	s.Handle("DELETE /v1/scannedGames/{id}", func(w http.ResponseWriter, r *http.Request) {
		removed := map[string]bool{}
		c.db.Get("removedGames", &removed)
		removed[r.PathValue("id")] = true
		c.db.Set("removedGames", removed)
		c.scanGames()
		httpx.OK(w)
	})
	s.Handle("PUT /v1/addGame", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			Name    string `json:"name"`
			ExePath string `json:"exe_path"`
		}](r)
		if p.ExePath == "" {
			httpx.Error(w, 400, "exe_path required")
			return
		}
		if p.Name == "" {
			p.Name = strings.TrimSuffix(filepath.Base(p.ExePath), filepath.Ext(p.ExePath))
		}
		var manual []ScannedGame
		c.db.Get("manualGames", &manual)
		id := "manual-" + httpx.RandomToken(6)
		manual = append(manual, ScannedGame{ID: id, Name: p.Name, ExePath: p.ExePath, Directory: filepath.Dir(p.ExePath), UniqueIdentifier: id, ScannerID: scannerManual})
		c.db.Set("manualGames", manual)
		c.scanGames()
		httpx.JSON(w, map[string]any{"id": id})
	})
}

// watchGames polls running processes and deploys the loadout bound to a
// game while it runs, returning to the previous loadout when it exits.
// Wayland gives no portable "foreground window" API, so a running bound
// game counts as active.
func (c *Core) watchGames(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	current := ""
	prevLoadout := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st := c.loadLoadouts()
		if !st.ProfilesEnabled || c.Setting("autoDeployLoadouts") == false {
			continue
		}
		bound := map[string]string{} // game id -> loadout id
		for _, l := range st.Loadouts {
			for _, b := range l.Bindings {
				if b.Type == "game" {
					bound[b.Value] = l.ID
				}
			}
		}
		if len(bound) == 0 {
			continue
		}
		running := runningGames(c.cachedGames())
		next := ""
		for _, g := range running {
			if id, ok := bound[g.ID]; ok {
				next = id
				break
			}
			if id, ok := bound[g.UniqueIdentifier]; ok {
				next = id
				break
			}
		}
		if next == current {
			continue
		}
		if next != "" {
			if current == "" {
				prevLoadout = st.ActiveID
			}
			slog.Info("game detected, deploying loadout", "loadout", next)
			c.DeployLoadout(next, false)
		} else if prevLoadout != "" {
			c.DeployLoadout(prevLoadout, false)
		}
		current = next
	}
}

// runningGames matches /proc entries against the library: Steam games by
// the SteamAppId environment variable, others by install directory.
func runningGames(games []ScannedGame) []ScannedGame {
	byApp := map[string]ScannedGame{}
	for _, g := range games {
		if g.steamAppID != "" {
			byApp[g.steamAppID] = g
		}
	}
	procs, _ := os.ReadDir("/proc")
	found := map[string]ScannedGame{}
	for _, p := range procs {
		if p.Name()[0] < '0' || p.Name()[0] > '9' {
			continue
		}
		dir := filepath.Join("/proc", p.Name())
		if env, err := os.ReadFile(filepath.Join(dir, "environ")); err == nil {
			for _, kv := range bytes.Split(env, []byte{0}) {
				if bytes.HasPrefix(kv, []byte("SteamAppId=")) {
					if g, ok := byApp[string(kv[len("SteamAppId="):])]; ok {
						found[g.ID] = g
					}
				}
			}
		}
		cmd, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil || len(cmd) == 0 {
			continue
		}
		line := string(bytes.ReplaceAll(cmd, []byte{0}, []byte{' '}))
		for _, g := range games {
			if g.Directory != "" && len(g.Directory) > 8 && strings.Contains(line, g.Directory) {
				found[g.ID] = g
			}
		}
	}
	out := make([]ScannedGame, 0, len(found))
	for _, g := range found {
		out = append(out, g)
	}
	return out
}
