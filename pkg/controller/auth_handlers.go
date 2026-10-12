package controller

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const sessionTTL = 7 * 24 * time.Hour

// DefaultSessionMaxLifetime bounds how long a browser session lives from
// its creation, however often the dashboard renews it. Override it with
// Server.WithSessionMaxLifetime.
const DefaultSessionMaxLifetime = 30 * 24 * time.Hour

var errSessionLifetimeExceeded = errors.New("session exceeded its maximum lifetime")

type logoutReq struct {
	SessionID string `json:"session_id"`
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	var req logoutReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.SessionID == "" {
		writeError(w, http.StatusBadRequest, errors.New("session_id required"))
		return
	}
	if err := s.store.DeleteSession(req.SessionID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type sessionResp struct {
	Principal string   `json:"principal"`
	Scopes    []string `json:"scopes"`
	CSRFToken string   `json:"csrf_token"`
	ExpiresAt int64    `json:"expires_at"`
	Team      string   `json:"team,omitempty"`
	Role      string   `json:"role,omitempty"`
	UserID    string   `json:"user_id,omitempty"`
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	// safety: Session header keeps session ids off the Bearer path so a session id never authenticates as a bearer token.
	raw := extractSessionHeader(r)
	if raw == "" {
		writeError(w, http.StatusUnauthorized, errors.New("session header required"))
		return
	}
	now := time.Now().UTC()
	sess, err := s.store.LookupSessionAndRenew(r.Context(), raw, now, sessionTTL, s.sessionMaxLifetime)
	if err != nil {
		// safety: a backend fault answered 401 would read as expiry and clear the dashboard's session cookies.
		if errors.Is(err, store.ErrSessionBackend) {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	// safety: an absolute cap ends a session the sliding TTL would otherwise renew forever.
	if s.sessionExpired(sess.CreatedAt, now) {
		_ = s.store.DeleteSession(raw)
		writeError(w, http.StatusUnauthorized, errSessionLifetimeExceeded)
		return
	}
	resp := sessionResp{
		Principal: sess.Principal,
		Scopes:    sess.Scopes,
		CSRFToken: sess.CSRFToken,
		ExpiresAt: sess.ExpiresAt.Unix(),
		Team:      string(sess.Team),
	}
	if sess.AccountID != "" {
		if !s.MultiTeam() {
			writeError(w, http.StatusUnauthorized, errAccountSessionsDisabled)
			return
		}
		role, err := s.memberRole(r.Context(), sess.Team, sess.AccountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		resp.Scopes, resp.Role, resp.UserID = ScopesForRole(role), string(role), sess.AccountID
	}
	writeJSON(w, http.StatusOK, resp)
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

func extractSessionHeader(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Session "
	if len(h) <= len(prefix) {
		return ""
	}
	if h[:len(prefix)] != prefix {
		return ""
	}
	return h[len(prefix):]
}

const (
	defaultRotateGrace = 24 * time.Hour
	// safety: a rotation must not mint an unbounded window in which the replaced token still authenticates.
	maxRotateGrace = 7 * 24 * time.Hour
)

type rotateReq struct {
	GraceSecs int64 `json:"grace_secs,omitempty"`
	TTLSecs   int64 `json:"ttl_secs,omitempty"`
}

type rotateResp struct {
	Token       string          `json:"token"`
	New         tokenRecordJSON `json:"new"`
	OldRevoked  int64           `json:"old_revoked_at"`
	OldReplaced string          `json:"old_replaced_by"`
}

func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	prefix := r.PathValue("prefix")
	var req rotateReq
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	grace := defaultRotateGrace
	if req.GraceSecs > 0 {
		grace = time.Duration(req.GraceSecs) * time.Second
	}
	if grace > maxRotateGrace {
		writeError(w, http.StatusBadRequest, fmt.Errorf(
			"grace_secs must not exceed %d (%s)",
			int64(maxRotateGrace.Seconds()), maxRotateGrace,
		))
		return
	}
	var ttl time.Duration
	if req.TTLSecs > 0 {
		ttl = time.Duration(req.TTLSecs) * time.Second
	} else {
		old, err := s.store.LookupTokenByPrefix(prefix)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		if old.ExpiresAt != nil {
			ttl = time.Until(*old.ExpiresAt)
			if ttl < 0 {
				ttl = 0
			}
		}
	}
	now := time.Now().UTC()
	raw, newTok, oldTok, err := s.store.RotateToken(prefix, grace, ttl, now)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.auth.Invalidate(prefix)
	s.logger.Info(
		"token rotated",
		"from_prefix", oldTok.Prefix,
		"to_prefix", newTok.Prefix,
		"principal", newTok.Principal,
		"grace_secs", int64(grace.Seconds()),
	)
	resp := rotateResp{
		Token:       raw,
		New:         tokenToJSON(newTok),
		OldReplaced: newTok.Prefix,
	}
	if oldTok.RevokedAt != nil {
		resp.OldRevoked = oldTok.RevokedAt.Unix()
	}
	writeJSON(w, http.StatusOK, resp)
}
