package local

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// Session TTL with sliding-window extension on active use within the
// last hour of life.
const (
	sessionTTL    = 12 * time.Hour
	sessionExtend = 1 * time.Hour
)

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

// handleLogin validates username+password and mints a session. The
// caller presents a password, not a bearer token, so the endpoint is
// unauthenticated.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, errors.New("username and password required"))
		return
	}
	now := time.Now().UTC()
	u, err := s.store.VerifyUser(req.Username, req.Password, now)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	scopes := []string{ScopeAdmin}
	rawSession, csrf, sess, err := s.store.CreateSession(u.Name, scopes, sessionTTL, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info("login",
		"principal", u.Name,
		"expires_at", sess.ExpiresAt.Unix(),
	)
	writeJSON(w, http.StatusOK, loginResp{
		SessionID: rawSession,
		CSRFToken: csrf,
		Principal: u.Name,
		Scopes:    scopes,
		ExpiresAt: sess.ExpiresAt.Unix(),
	})
}

type logoutReq struct {
	SessionID string `json:"session_id"`
}

// handleLogout deletes the session row. Idempotent; no scope check.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	var req logoutReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

// handleSession resolves a session id (passed in the Authorization:
// Session header, NOT as a Bearer token) to the principal + scopes +
// CSRF token bound to it. Unauthed: the caller presents the session
// id itself, which is the same trust the cookie grants.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	// `Authorization: Session <raw>` keeps session lookup off the
	// Bearer path so a session id never authenticates as a bearer.
	raw := extractSessionHeader(r)
	if raw == "" {
		writeError(w, http.StatusUnauthorized, errors.New("session header required"))
		return
	}
	now := time.Now().UTC()
	sess, err := s.store.LookupSession(raw, now)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	// Sliding window: within sessionExtend of expiry, push expires_at
	// out by another sessionTTL.
	if sess.ExpiresAt.Sub(now) < sessionExtend {
		_ = s.store.ExtendSession(sess.ID, sessionTTL, now)
		sess.ExpiresAt = now.Add(sessionTTL)
	}
	writeJSON(w, http.StatusOK, sessionResp{
		Principal: sess.Principal,
		Scopes:    sess.Scopes,
		CSRFToken: sess.CSRFToken,
		ExpiresAt: sess.ExpiresAt.Unix(),
	})
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

// --- users CRUD ---

type createUserReq struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

type userJSON struct {
	Name        string `json:"name"`
	CreatedAt   int64  `json:"created_at"`
	LastLoginAt *int64 `json:"last_login_at,omitempty"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	u, err := s.store.CreateUser(req.Name, req.Password, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logger.Info("user created", "name", u.Name)
	writeJSON(w, http.StatusCreated, userJSON{
		Name:      u.Name,
		CreatedAt: u.CreatedAt.Unix(),
	})
}

// handleCreateUserOrBootstrap is the outer-router entry for
// POST /api/v1/users. When the users table is empty it accepts an
// unauthenticated first-admin create; otherwise it delegates to the
// admin-scoped handler. CreateFirstUser re-checks emptiness in-tx so
// two concurrent bootstrap POSTs cannot both succeed.
func (s *Server) handleCreateUserOrBootstrap(w http.ResponseWriter, r *http.Request) {
	if !s.bootstrapAllowed() {
		s.authMiddleware().Middleware(
			requireScope(ScopeAdmin, http.HandlerFunc(s.handleCreateUser)),
		).ServeHTTP(w, r)
		return
	}
	var req createUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	u, err := s.store.CreateFirstUser(req.Name, req.Password, time.Now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrBootstrapClosed) {
			// Another request won the race; close the local cache and
			// 409 so the web pod reloads /login.
			s.markBootstrapClosed()
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logger.Warn("bootstrap signup accepted: first admin created via unauthenticated /login",
		"name", u.Name)
	s.markBootstrapClosed()
	writeJSON(w, http.StatusCreated, userJSON{
		Name:      u.Name,
		CreatedAt: u.CreatedAt.Unix(),
	})
}

// handleBootstrapNeeded is the unauthenticated probe the web pod hits
// before rendering /login. Cached for 60s; latched false once any
// user exists.
func (s *Server) handleBootstrapNeeded(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"needed": s.bootstrapAllowed()})
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]userJSON, 0, len(users))
	for _, u := range users {
		row := userJSON{Name: u.Name, CreatedAt: u.CreatedAt.Unix()}
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
	if err := s.store.DeleteUser(name); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- token rotation ---

type rotateReq struct {
	GraceSecs int64 `json:"grace_secs,omitempty"` // default 24h
	TTLSecs   int64 `json:"ttl_secs,omitempty"`   // 0 = use the old token's remaining TTL
}

type rotateResp struct {
	Token       string          `json:"token"`
	New         tokenRecordJSON `json:"new"`
	OldRevoked  int64           `json:"old_revoked_at"` // unix seconds; when the old token stops working
	OldReplaced string          `json:"old_replaced_by"`
}

// handleRotateToken creates a replacement token and sets the old
// token's revoked_at to now+grace so in-flight callers have time to
// swap.
func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	prefix := r.PathValue("prefix")
	var req rotateReq
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	grace := 24 * time.Hour
	if req.GraceSecs > 0 {
		grace = time.Duration(req.GraceSecs) * time.Second
	}
	var ttl time.Duration
	if req.TTLSecs > 0 {
		ttl = time.Duration(req.TTLSecs) * time.Second
	} else {
		// Preserve the old token's remaining lifetime.
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
	s.logger.Info("token rotated",
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
