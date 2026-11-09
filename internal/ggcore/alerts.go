package ggcore

import (
	"net/http"
	"time"

	"ggl/internal/httpx"
)

// Snoozable alerts are banners the client shows until the user snoozes
// them for a while (the QuickSet page's "not currently set as Sonar
// device", for one), as the GG core's snoozable_alerts service keeps them.

type snoozableAlert struct {
	Key                   string     `json:"key"`
	DefaultSnoozeDuration int        `json:"defaultSnoozeDuration"` // days
	SnoozedAt             *time.Time `json:"snoozedAt"`
	SnoozedFor            *int       `json:"snoozedFor"`
	ShouldShow            bool       `json:"shouldShow"`
}

const defaultSnoozeDays = 30

func (c *Core) alert(key string) snoozableAlert {
	snoozed := map[string]snoozableAlert{}
	c.db.Get("alerts", &snoozed)
	a, ok := snoozed[key]
	if !ok {
		a = snoozableAlert{Key: key}
	}
	a.DefaultSnoozeDuration = defaultSnoozeDays
	a.ShouldShow = a.SnoozedAt == nil || a.SnoozedFor == nil ||
		time.Since(*a.SnoozedAt) > time.Duration(*a.SnoozedFor)*24*time.Hour
	return a
}

func (c *Core) alertRoutes() {
	s := c.srv
	s.Handle("GET /v1/alerts/{id}", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, c.alert(r.PathValue("id")))
	})
	snooze := func(w http.ResponseWriter, r *http.Request) {
		p, _ := decode[struct {
			Days *int `json:"snoozeFor"`
		}](r)
		days := defaultSnoozeDays
		if p.Days != nil && *p.Days > 0 {
			days = *p.Days
		}
		key, now := r.PathValue("id"), time.Now().UTC()
		snoozed := map[string]snoozableAlert{}
		c.db.Get("alerts", &snoozed)
		snoozed[key] = snoozableAlert{Key: key, SnoozedAt: &now, SnoozedFor: &days}
		c.db.Set("alerts", snoozed)
		httpx.JSON(w, c.alert(key))
	}
	s.Handle("PUT /v1/alerts/{id}", snooze)
	s.Handle("POST /v1/alerts/{id}", snooze)
}
