package controller

import (
	"errors"
	"time"
)

const sessionTTL = 7 * 24 * time.Hour

// defaultSessionMaxLifetime bounds how long a browser session lives from
// its creation, however often the dashboard renews it. Override it with
// Server.WithSessionMaxLifetime.
const defaultSessionMaxLifetime = 30 * 24 * time.Hour

var errSessionLifetimeExceeded = errors.New("session exceeded its maximum lifetime")

type sessionResp struct {
	Principal string   `json:"principal"`
	Scopes    []string `json:"scopes"`
	CSRFToken string   `json:"csrf_token"`
	ExpiresAt int64    `json:"expires_at"`
	Team      string   `json:"team,omitempty"`
	Role      string   `json:"role,omitempty"`
	UserID    string   `json:"user_id,omitempty"`
}

func (s *Server) sessionExpired(createdAt, now time.Time) bool {
	// safety: a future creation time makes the lifetime clamp inert, so a backwards clock jump renews forever.
	if createdAt.After(now) {
		return true
	}
	return s.sessionMaxLifetime > 0 && !now.Before(createdAt.Add(s.sessionMaxLifetime))
}

func (s *Server) sessionInitialTTL() time.Duration {
	if s.sessionMaxLifetime <= 0 {
		return sessionTTL
	}
	return min(s.sessionMaxLifetime, sessionTTL)
}

const (
	defaultRotateGrace = 24 * time.Hour
	// safety: a rotation must not mint an unbounded window in which the replaced token still authenticates.
	maxRotateGrace = 7 * 24 * time.Hour
)
