package ggcore

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"os/user"
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

// GG runs with a local profile: the Linux user, signed in, so what GG keeps
// for signed-in accounts (Sonar, Moments) works offline. Signing out goes
// back to the login screen, where "skip" signs the profile in again.
// SteelSeries account sign-in itself is not available.

func (c *Core) currentUser() User {
	u := User{ID: 1, ApplicationAccess: []string{}, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	c.db.Get("user", &u)
	if u.ApplicationAccess == nil {
		u.ApplicationAccess = []string{}
	}
	if u.UUID == "" {
		b := make([]byte, 16)
		rand.Read(b)
		b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
		u.UUID = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
		c.db.Set("user", u)
	}
	if u.Username == "" {
		if lu, err := user.Current(); err == nil {
			u.Username, u.Name = lu.Username, lu.Name
			if u.Name == "" {
				u.Name = lu.Username
			}
		}
	}
	var signedOut bool
	c.db.Get("localProfileSignedOut", &signedOut)
	u.LoggedIn, u.LoginSkipped = !signedOut, false
	return u
}

func (c *Core) setSignedIn(in bool) User {
	c.db.Set("localProfileSignedOut", !in)
	u := c.currentUser()
	c.db.Set("user", u)
	if in {
		c.Broadcast("EVENT_USER_LOGGED_IN", map[string]any{"user": u})
	} else {
		c.Broadcast("EVENT_USER_LOGGED_OUT", map[string]any{"user": u})
	}
	return u
}

func (c *Core) userRoutes() {
	s := c.srv
	s.Handle("GET /user", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"user": c.currentUser()})
	})
	s.Handle("POST /login/skip", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"user": c.setSignedIn(true)})
	})
	// SteelSeries account sign-in goes through SteelSeries' cloud OAuth
	// service. gg-linux works offline with the local profile, so sign-in is
	// reported as unavailable and the client offers "skip".
	s.Handle("POST /login", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusServiceUnavailable, "SteelSeries account sign-in is not available on Linux yet")
	})
	s.Handle("POST /user", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusServiceUnavailable, "SteelSeries account creation is not available on Linux yet")
	})
	s.Handle("GET /logout", func(w http.ResponseWriter, r *http.Request) {
		c.setSignedIn(false)
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
