package controller

import (
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
	Metered    bool     `json:"metered,omitempty"`
}

func tokenToJSON(t *store.Token) tokenRecordJSON {
	out := tokenRecordJSON{
		Prefix:     t.Prefix,
		Principal:  t.Principal,
		Kind:       t.Kind,
		Scopes:     t.Scopes,
		CreatedAt:  t.CreatedAt.Unix(),
		ReplacedBy: t.ReplacedBy,
		Metered:    t.Metered,
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

type setTokenMeteredReq struct {
	Metered bool `json:"metered"`
}

// safety: metering is recorded on the token, so a pool already running can be
// marked without a new credential.
func (s *Server) handleSetTokenMetered(w http.ResponseWriter, r *http.Request) {
	prefix := r.PathValue("prefix")
	var req setTokenMeteredReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.SetTokenMetered(r.Context(), prefix, req.Metered); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tok, err := s.store.LookupTokenByPrefix(prefix)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	who := authwire.AnonymousPrincipal
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil {
		who = p.Name
	}
	s.logger.Info("token metering changed", "prefix", prefix, "metered", req.Metered, "by", who)
	writeJSON(w, http.StatusOK, tokenToJSON(tok))
}

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

func (s *Server) handleLookupTokenByPrefix(w http.ResponseWriter, r *http.Request) {
	prefix := r.PathValue("token")
	tok, err := s.store.LookupTokenByPrefix(prefix)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, tokenToJSON(tok))
}
