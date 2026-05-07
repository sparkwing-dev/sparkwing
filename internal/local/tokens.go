package local

// HTTP handlers for the /api/v1/tokens CRUD surface. Raw tokens from
// POST /api/v1/tokens are emitted in the response ONCE and never
// echoed back; callers who lose the raw can only revoke and recreate.

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// tokenRecordJSON is the read-side projection. Never includes the
// hash or the raw value.
type tokenRecordJSON struct {
	Prefix     string   `json:"prefix"`
	Principal  string   `json:"principal"`
	Kind       string   `json:"kind"`
	Scopes     []string `json:"scopes"`
	CreatedAt  int64    `json:"created_at"`
	ExpiresAt  *int64   `json:"expires_at,omitempty"`
	LastUsedAt *int64   `json:"last_used_at,omitempty"`
	RevokedAt  *int64   `json:"revoked_at,omitempty"`
	ReplacedBy string   `json:"replaced_by,omitempty"`
}

func tokenToJSON(t *store.Token) tokenRecordJSON {
	out := tokenRecordJSON{
		Prefix:     t.Prefix,
		Principal:  t.Principal,
		Kind:       t.Kind,
		Scopes:     t.Scopes,
		CreatedAt:  t.CreatedAt.Unix(),
		ReplacedBy: t.ReplacedBy,
	}
	if t.ExpiresAt != nil {
		v := t.ExpiresAt.Unix()
		out.ExpiresAt = &v
	}
	if t.LastUsedAt != nil {
		v := t.LastUsedAt.Unix()
		out.LastUsedAt = &v
	}
	if t.RevokedAt != nil {
		v := t.RevokedAt.Unix()
		out.RevokedAt = &v
	}
	return out
}

type createTokenReq struct {
	Principal string   `json:"principal"`
	Kind      string   `json:"kind"`
	Scopes    []string `json:"scopes"`
	TTLSecs   int64    `json:"ttl_secs,omitempty"`
}

type createTokenResp struct {
	Token    string          `json:"token"` // raw; emitted ONCE
	Metadata tokenRecordJSON `json:"metadata"`
}

// handleCreateToken mints a new token. The raw value is returned ONCE
// in the response body; callers MUST stash it before acknowledging.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req createTokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Principal == "" {
		writeError(w, http.StatusBadRequest, errors.New("principal required"))
		return
	}
	if req.Kind == "" {
		writeError(w, http.StatusBadRequest, errors.New("kind required (user|runner|service)"))
		return
	}
	ttl := time.Duration(req.TTLSecs) * time.Second
	now := time.Now().UTC()
	raw, tok, err := s.store.CreateToken(req.Principal, req.Kind, req.Scopes, ttl, now)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logger.Info("token created",
		"principal", tok.Principal,
		"kind", tok.Kind,
		"prefix", tok.Prefix,
		"scopes", tok.Scopes,
	)
	writeJSON(w, http.StatusCreated, createTokenResp{
		Token:    raw,
		Metadata: tokenToJSON(tok),
	})
}

// handleListTokens returns all tokens (prefix + metadata, no secrets).
// Query params:
//
//	kind=user|runner|service
//	include_revoked=1
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	includeRevoked := r.URL.Query().Get("include_revoked") == "1"
	tokens, err := s.store.ListTokens(kind, includeRevoked)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]tokenRecordJSON, 0, len(tokens))
	for i := range tokens {
		out = append(out, tokenToJSON(&tokens[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

// handleLookupTokenByPrefix returns metadata for a single token given
// its non-secret prefix.
func (s *Server) handleLookupTokenByPrefix(w http.ResponseWriter, r *http.Request) {
	prefix := r.PathValue("prefix")
	tok, err := s.store.LookupTokenByPrefix(prefix)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, tokenToJSON(tok))
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	prefix := r.PathValue("prefix")
	if err := s.store.RevokeToken(prefix, time.Now().UTC()); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	p, _ := PrincipalFromContext(r.Context())
	who := "unauthed"
	if p != nil {
		who = p.Name
	}
	s.logger.Info("token revoked", "prefix", prefix, "by", who)
	w.WriteHeader(http.StatusNoContent)
}

// whoamiResp is what GET /api/v1/auth/whoami returns.
type whoamiResp struct {
	Principal   string   `json:"principal"`
	Kind        string   `json:"kind"`
	Scopes      []string `json:"scopes"`
	TokenPrefix string   `json:"token_prefix,omitempty"`
}

// handleWhoami reflects the calling principal back to the caller. No
// scope check: any valid token suffices to learn its own identity.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		// Auth disabled: return a synthetic "unauthed" principal so
		// callers don't have to special-case the empty response.
		writeJSON(w, http.StatusOK, whoamiResp{
			Principal: "unauthed",
			Kind:      "none",
			Scopes:    nil,
		})
		return
	}
	writeJSON(w, http.StatusOK, whoamiResp{
		Principal:   p.Name,
		Kind:        p.Kind,
		Scopes:      p.Scopes,
		TokenPrefix: p.TokenPrefix,
	})
}
