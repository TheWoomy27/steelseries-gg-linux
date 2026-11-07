package ggcore

import (
	"net/http"
	"time"

	"ggl/internal/httpx"
)

// User mirrors publicapi.jsonUser.
type User struct {
	ID                          int      `json:"id"`
	Name                        string   `json:"name"`
	Username                    string   `json:"username"`
	Discriminator               string   `json:"discriminator"`
	IsUsernameChanged           bool     `json:"isUsernameChanged"`
	AvatarSmallURL              string   `json:"avatarSmallUrl"`
	Email                       string   `json:"email"`
	LoggedIn                    bool     `json:"loggedIn"`
	LoginSkipped                bool     `json:"loginSkipped"`
	UUID                        string   `json:"uuid"`
	ApplicationAccess           []string `json:"applicationAccess"`
	CreatedAt                   string   `json:"createdAt"`
	LoggedInWithoutRefreshToken bool     `json:"loggedInWithoutRefreshToken"`
}

func (c *Core) currentUser() User {
	u := User{ID: 1, ApplicationAccess: []string{}, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	c.db.Get("user", &u)
	if u.ApplicationAccess == nil {
		u.ApplicationAccess = []string{}
	}
	return u
}

func (c *Core) userRoutes() {
	s := c.srv
	s.Handle("GET /user", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"user": c.currentUser()})
	})
	s.Handle("POST /login/skip", func(w http.ResponseWriter, r *http.Request) {
		u := c.currentUser()
		u.LoginSkipped = true
		c.db.Set("user", u)
		c.Broadcast("EVENT_USER_LOGGED_IN", map[string]any{"user": u})
		httpx.JSON(w, map[string]any{"user": u})
	})
	// SteelSeries account sign-in goes through SteelSeries' cloud OAuth
	// service. gg-linux works fully offline, so sign-in is reported as
	// unavailable and the client offers "skip".
	s.Handle("POST /login", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusServiceUnavailable, "SteelSeries account sign-in is not available on Linux yet")
	})
	s.Handle("POST /user", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusServiceUnavailable, "SteelSeries account creation is not available on Linux yet")
	})
	s.Handle("GET /logout", func(w http.ResponseWriter, r *http.Request) {
		u := c.currentUser()
		u.LoggedIn = false
		c.db.Set("user", u)
		c.Broadcast("EVENT_USER_LOGGED_OUT", map[string]any{"user": u})
		httpx.OK(w)
	})
	s.Handle("PATCH /user", func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			Name     string `json:"name"`
			Username string `json:"username"`
		}](r)
		u := c.currentUser()
		if p.Name != "" {
			u.Name = p.Name
		}
		if p.Username != "" {
			u.Username, u.IsUsernameChanged = p.Username, true
		}
		c.db.Set("user", u)
		httpx.JSON(w, map[string]any{"user": u})
	})
	s.Handle("POST /user/reload", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"user": c.currentUser()})
	})
	s.Handle("GET /user/token", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusUnauthorized, "no access token")
	})
	s.Handle("GET /user/token/refresh", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusUnauthorized, "no access token")
	})
	s.Handle("GET /user/linkedAccounts", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"linkedAccounts": []any{}})
	})
	s.Handle("GET /user/avatars", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"avatars": []any{}})
	})
}
