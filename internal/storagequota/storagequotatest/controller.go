// Package storagequotatest is a stand-in for the controller's storage
// counter routes, for tests of the services that call them. It keeps the
// counts in memory under one lock with the controller's arithmetic: a free
// team's reservation fits used plus reserved within its share, and a
// download fits the team's day within its cap.
package storagequotatest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
)

type key struct {
	team string
	kind storagequota.Kind
}

type hold struct {
	key   key
	bytes int64
}

// Controller answers /internal/storage/*, /internal/downloads/charge and
// /internal/egress/totals.
type Controller struct {
	// Share is each free team's room in each store; DownloadCap its bytes
	// a day. Zero DownloadCap is no cap.
	Share, DownloadCap int64
	// Tiers names a team's tier; a team it does not name is free.
	Tiers map[string]storagequota.Tier
	// Token, when set, is the only bearer the fake answers.
	Token string

	mu       sync.Mutex
	down     bool
	auths    []string
	used     map[key]int64
	reserved map[key]int64
	holds    map[string]hold
	next     int
	days     map[string]int64
	egress   map[string]int64
}

// New returns a fake holding free teams to share bytes per store and
// downloadCap bytes a day.
func New(share, downloadCap int64) *Controller {
	return &Controller{
		Share: share, DownloadCap: downloadCap, Tiers: map[string]storagequota.Tier{},
		used: map[key]int64{}, reserved: map[key]int64{}, holds: map[string]hold{},
		days: map[string]int64{}, egress: map[string]int64{},
	}
}

// SetDown makes every route answer 500, as a controller whose database is
// unreachable does.
func (c *Controller) SetDown(down bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.down = down
}

// Held reports what team stores and holds reserved in kind.
func (c *Controller) Held(team string, kind storagequota.Kind) (used, reserved int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key{team, kind}
	return c.used[k], c.reserved[k]
}

// Downloaded reports what team was charged for downloads.
func (c *Controller) Downloaded(team string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.days[team]
}

// Egress reports the stored egress total for a period, day or month.
func (c *Controller) Egress(period string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.egress[period]
}

// Auths lists the Authorization header of every call so far.
func (c *Controller) Auths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.auths...)
}

func (c *Controller) tier(team string) storagequota.Tier {
	if t, ok := c.Tiers[team]; ok {
		return t
	}
	return storagequota.TierFree
}

// safety: an encode that fails answers 500, so a caller never decodes a
// half-written body as an empty answer.
func writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func refuse(w http.ResponseWriter, status int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": fmt.Sprintf(format, args...)})
}

func (c *Controller) commit(k key, team, reservation string, n int64) {
	if h, ok := c.holds[reservation]; ok && h.key.team == team {
		c.reserved[h.key] -= h.bytes
		delete(c.holds, reservation)
	}
	c.used[k] = max(c.used[k]+n, 0)
}

func (c *Controller) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.auths = append(c.auths, r.Header.Get("Authorization"))
	if c.down {
		refuse(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if c.Token != "" && r.Header.Get("Authorization") != "Bearer "+c.Token {
		refuse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Team        string            `json:"team"`
		Store       storagequota.Kind `json:"store"`
		Bytes       int64             `json:"bytes"`
		UpTo        bool              `json:"up_to"`
		Reservation string            `json:"reservation"`
		Record      bool              `json:"record"`
		NextBytes   int64             `json:"next_bytes"`
		storagequota.EgressTotals
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		refuse(w, http.StatusBadRequest, "%v", err)
		return
	}
	k := key{req.Team, req.Store}
	path := r.URL.Path
	if path == "/internal/storage/commit" && req.NextBytes > 0 {
		c.commit(k, req.Team, req.Reservation, req.Bytes)
		req.Bytes, req.UpTo, path = req.NextBytes, true, "/internal/storage/reserve"
	}
	switch path {
	case "/internal/storage/reserve":
		tier := c.tier(req.Team)
		granted := req.Bytes
		switch tier {
		case storagequota.TierNone:
			refuse(w, http.StatusPaymentRequired, "free storage is paused; buy credits or join the waitlist")
			return
		case storagequota.TierFree:
			room := c.Share - c.used[k] - c.reserved[k]
			if req.UpTo && (granted <= 0 || granted > room) {
				granted = room
			}
			if room <= 0 || granted > room {
				refuse(w, http.StatusRequestEntityTooLarge,
					"storage quota exceeded: team %s holds %d of its %d bytes and this write adds %d; add credits to store more",
					req.Team, c.used[k]+c.reserved[k], c.Share, req.Bytes)
				return
			}
		}
		c.next++
		id := fmt.Sprintf("sr_%d", c.next)
		c.holds[id] = hold{key: k, bytes: max(granted, 0)}
		c.reserved[k] += max(granted, 0)
		writeJSON(w, map[string]any{
			"reservation": id, "tier": tier, "granted_bytes": max(granted, 0),
			"unlimited": tier == storagequota.TierFunded && req.UpTo && req.Bytes <= 0,
		})
	case "/internal/storage/commit":
		c.commit(k, req.Team, req.Reservation, req.Bytes)
		w.WriteHeader(http.StatusNoContent)
	case "/internal/storage/release":
		if h, ok := c.holds[req.Reservation]; ok && h.key.team == req.Team {
			c.reserved[h.key] -= h.bytes
			delete(c.holds, req.Reservation)
		}
		w.WriteHeader(http.StatusNoContent)
	case "/internal/downloads/charge":
		tier := c.tier(req.Team)
		if !req.Record && c.DownloadCap > 0 && tier != storagequota.TierFunded && c.days[req.Team]+max(req.Bytes, 1) > c.DownloadCap {
			w.Header().Set("Retry-After", "3600")
			refuse(w, http.StatusTooManyRequests, "daily download cap reached: team %s downloaded %d of its %d bytes",
				req.Team, c.days[req.Team], c.DownloadCap)
			return
		}
		c.days[req.Team] += req.Bytes
		writeJSON(w, map[string]any{"tier": tier, "day_bytes": c.days[req.Team], "cap_bytes": c.DownloadCap})
	case "/internal/egress/totals":
		t := req.EgressTotals
		c.egress[t.Day] = max(c.egress[t.Day], t.DayBytes)
		c.egress[t.Month] = max(c.egress[t.Month], t.MonthBytes)
		t.DayBytes, t.MonthBytes = c.egress[t.Day], c.egress[t.Month]
		writeJSON(w, t)
	default:
		http.NotFound(w, r)
	}
}
