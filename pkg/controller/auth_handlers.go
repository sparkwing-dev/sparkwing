package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	sessionTTL    = 12 * time.Hour
	sessionExtend = 1 * time.Hour
)

// DefaultSessionMaxLifetime bounds how long a browser session lives from
// its creation, however often the dashboard renews it. Override it with
// Server.WithSessionMaxLifetime.
const DefaultSessionMaxLifetime = 7 * 24 * time.Hour

var errSessionLifetimeExceeded = errors.New("session exceeded its maximum lifetime")

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResp struct {
	SessionID string   `json:"session_id"`
	CSRFToken string   `json:"csrf_token"`
	Principal string   `json:"principal"`
	Scopes    []string `json:"scopes"`
	ExpiresAt int64    `json:"expires_at"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, errors.New("username and password required"))
		return
	}
	now := time.Now().UTC()
	client := s.loginLimit.client(r)
	// safety: a drained failure budget answers before VerifyUser, so guessing one account costs no argon2 work.
	if !s.loginLimit.accountAllowed(req.Username, client, now) {
		writeRetryAfter(w, loginFailureWindow, "too many failed login attempts for this account")
		return
	}
	u, err := s.store.VerifyUser(req.Username, req.Password, now)
	if err != nil {
		if !errors.Is(err, store.ErrInvalidCredentials) {
			s.writeLoginUnavailable(w, err)
			return
		}
		s.loginLimit.accountFailed(req.Username, client, now)
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	rawSession, csrf, sess, err := s.store.CreateSession(u.Name, u.Scopes, sessionTTL, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info(
		"login",
		"principal", u.Name,
		"expires_at", sess.ExpiresAt.Unix(),
	)
	writeJSON(w, http.StatusOK, loginResp{
		SessionID: rawSession,
		CSRFToken: csrf,
		Principal: u.Name,
		Scopes:    u.Scopes,
		ExpiresAt: sess.ExpiresAt.Unix(),
	})
}

// safety: the caller is unauthenticated, so a login the controller could not decide answers a generic 503.
func (s *Server) writeLoginUnavailable(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrHashingBusy) {
		writeRetryAfterStatus(w, http.StatusServiceUnavailable, authBusyRetryAfter,
			"authentication is busy, retry shortly")
		return
	}
	s.logger.Error("login.unavailable", "error", err.Error())
	writeRetryAfterStatus(w, http.StatusServiceUnavailable, authUnavailableRetryAfter,
		"authentication is temporarily unavailable")
}

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
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	// safety: Session header keeps session ids off the Bearer path so a session id never authenticates as a bearer token.
	raw := extractSessionHeader(r)
	if raw == "" {
		writeError(w, http.StatusUnauthorized, errors.New("session header required"))
		return
	}
	now := time.Now().UTC()
	sess, err := s.store.LookupSession(raw, now)
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
	if sess.ExpiresAt.Sub(now) < sessionExtend {
		ttl := s.sessionExtensionTTL(sess.CreatedAt, now)
		_ = s.store.ExtendSession(sess.ID, ttl, now)
		sess.ExpiresAt = now.Add(ttl)
	}
	writeJSON(w, http.StatusOK, sessionResp{
		Principal: sess.Principal,
		Scopes:    sess.Scopes,
		CSRFToken: sess.CSRFToken,
		ExpiresAt: sess.ExpiresAt.Unix(),
	})
}

func (s *Server) sessionExpired(createdAt, now time.Time) bool {
	return s.sessionMaxLifetime > 0 && !now.Before(createdAt.Add(s.sessionMaxLifetime))
}

func (s *Server) sessionExtensionTTL(createdAt, now time.Time) time.Duration {
	if s.sessionMaxLifetime <= 0 {
		return sessionTTL
	}
	return min(createdAt.Add(s.sessionMaxLifetime).Sub(now), sessionTTL)
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

type createUserReq struct {
	Name     string   `json:"name"`
	Password string   `json:"password"`
	Scopes   []string `json:"scopes,omitempty"`
}

type userJSON struct {
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes"`
	CreatedAt   int64    `json:"created_at"`
	LastLoginAt *int64   `json:"last_login_at,omitempty"`
}

func requestedUserScopes(req createUserReq) ([]string, error) {
	if len(req.Scopes) == 0 {
		// safety: an omitted scope set grants admin, matching what an operator
		// who names no scopes has always received from this route.
		return []string{ScopeAdmin}, nil
	}
	// safety: a blank or repeated entry is dropped on the way to storage, so the
	// account would sign in holding a narrower scope set than the operator named.
	seen := make(map[string]struct{}, len(req.Scopes))
	for _, s := range req.Scopes {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			return nil, fmt.Errorf("empty scope (valid: %s)", strings.Join(allScopes, ", "))
		}
		if _, dup := seen[trimmed]; dup {
			return nil, fmt.Errorf("duplicate scope %s", strconv.Quote(trimmed))
		}
		seen[trimmed] = struct{}{}
	}
	if err := validateScopes(req.Scopes); err != nil {
		return nil, err
	}
	return req.Scopes, nil
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	scopes, err := requestedUserScopes(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	u, err := s.store.CreateUser(req.Name, req.Password, scopes, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logger.Info("user created", "name", u.Name, "scopes", u.Scopes)
	writeJSON(w, http.StatusCreated, userJSON{
		Name:      u.Name,
		Scopes:    u.Scopes,
		CreatedAt: u.CreatedAt.Unix(),
	})
}

func (s *Server) handleCreateUserOrBootstrap(w http.ResponseWriter, r *http.Request) {
	if !s.bootstrapAllowed() {
		s.handleCreateUser(w, r)
		return
	}
	var req createUserReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	u, err := s.store.CreateFirstUser(req.Name, req.Password, []string{ScopeAdmin}, time.Now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrBootstrapClosed) {
			s.markBootstrapClosed()
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if principal, ok := PrincipalFromContext(r.Context()); ok {
		s.logger.Info("bootstrap signup accepted: first admin created by authenticated principal",
			"name", u.Name, "principal", principal.label())
	} else {
		s.logger.Warn("bootstrap signup accepted: first admin created while controller authentication is disabled",
			"name", u.Name)
	}
	s.markBootstrapClosed()
	writeJSON(w, http.StatusCreated, userJSON{
		Name:      u.Name,
		Scopes:    u.Scopes,
		CreatedAt: u.CreatedAt.Unix(),
	})
}

func (s *Server) handleBootstrapNeeded(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"needed": !s.AuthEnabled() && s.bootstrapAllowed(),
	})
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]userJSON, 0, len(users))
	for _, u := range users {
		row := userJSON{Name: u.Name, Scopes: u.Scopes, CreatedAt: u.CreatedAt.Unix()}
		if u.LastLoginAt != nil {
			v := u.LastLoginAt.Unix()
			row.LastLoginAt = &v
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	who := authwire.AnonymousPrincipal
	keep := ""
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil {
		who = p.Name
		// safety: revoking the requesting token would lock the operator out mid-incident.
		keep = p.TokenPrefix
	}
	sessions, revoked, err := s.store.DeleteUser(name, keep, time.Now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		s.logger.Error("user delete failed", "name", name, "by", who, "err", err)
		writeError(w, http.StatusInternalServerError, errors.New("could not delete user"))
		return
	}
	for _, prefix := range revoked {
		s.auth.Invalidate(prefix)
	}
	s.logger.Info(
		"user deleted",
		"name", name,
		"sessions", sessions,
		"revoked_prefixes", revoked,
		"by", who,
	)
	w.WriteHeader(http.StatusNoContent)
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
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
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
