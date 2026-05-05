package controller

// Secrets CRUD HTTP handlers. All routes require ScopeAdmin. Raw
// values never leave the server except via GET /api/v1/secrets/{name};
// LIST blanks the value field on every response row.

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/secrets"
)

// secretSetReq is the wire body for POST /api/v1/secrets.
type secretSetReq struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Masked: nil pointer means the client didn't supply the field;
	// the server defaults to true. Explicit false writes the entry as
	// non-masked config.
	Masked *bool `json:"masked,omitempty"`
}

// secretJSON is the non-value metadata the list endpoint emits. The
// get endpoint reuses the same struct with Value populated so the
// client sees one stable shape.
type secretJSON struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	Principal string `json:"principal"`
	Masked    bool   `json:"masked"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func (s *Server) handleCreateSecret(w http.ResponseWriter, r *http.Request) {
	var req secretSetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := secrets.ValidateName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Attribute the write to the calling principal for audit. Falls
	// back to "anonymous" when auth is disabled.
	principal := "anonymous"
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil {
		principal = p.Name
	}
	// Encrypt at rest when a cipher is configured.
	stored := req.Value
	if s.secretsCipher != nil {
		sealed, sErr := s.secretsCipher.Seal(req.Value)
		if sErr != nil {
			writeError(w, http.StatusInternalServerError, sErr)
			return
		}
		stored = sealed
	}
	// Default masked=true when the caller didn't supply the field.
	masked := true
	if req.Masked != nil {
		masked = *req.Masked
	}
	if err := s.store.CreateOrReplaceSecret(req.Name, stored, principal, masked, time.Now().UTC()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info("secret written", "name", req.Name, "principal", principal,
		"encrypted", s.secretsCipher != nil, "masked", masked)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sec, err := s.store.GetSecret(name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Decrypt before responding. Cipher.Open is a no-op for rows
	// without the envelope prefix. A row with an envelope but no
	// cipher configured produces a 500 -- the operator removed the
	// key without a re-encrypt step, which is not silently recoverable.
	plain := sec.Value
	if secrets.IsEncrypted(plain) {
		opened, oerr := s.secretsCipher.Open(plain)
		if oerr != nil {
			writeError(w, http.StatusInternalServerError, oerr)
			return
		}
		plain = opened
	}
	writeJSON(w, http.StatusOK, secretJSON{
		Name:      sec.Name,
		Value:     plain,
		Principal: sec.Principal,
		Masked:    sec.Masked,
		CreatedAt: sec.CreatedAt.Unix(),
		UpdatedAt: sec.UpdatedAt.Unix(),
	})
}

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	secs, err := s.store.ListSecrets()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]secretJSON, 0, len(secs))
	for _, sec := range secs {
		// Drop Value -- list must never leak secret material.
		out = append(out, secretJSON{
			Name:      sec.Name,
			Principal: sec.Principal,
			Masked:    sec.Masked,
			CreatedAt: sec.CreatedAt.Unix(),
			UpdatedAt: sec.UpdatedAt.Unix(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"secrets": out})
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.DeleteSecret(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
