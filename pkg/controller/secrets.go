package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type secretSetReq struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Repo  string `json:"repo,omitempty"`
	// safety: nil defaults to masked; only an explicit false stores plain config.
	Masked *bool `json:"masked,omitempty"`
}

type secretJSON struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	Principal string `json:"principal"`
	Repo      string `json:"repo,omitempty"`
	Masked    bool   `json:"masked"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func (s *Server) handleCreateSecret(w http.ResponseWriter, r *http.Request) {
	var req secretSetReq
	if err := decodeJSONLimit(r, &req, maxSecretJSONBody); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := secrets.ValidateName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	principal := "anonymous"
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil {
		principal = p.Name
	}
	stored := req.Value
	if s.secretsCipher != nil {
		sealed, sErr := s.secretsCipher.Seal(req.Value)
		if sErr != nil {
			writeError(w, http.StatusInternalServerError, sErr)
			return
		}
		stored = sealed
	}
	masked := true
	if req.Masked != nil {
		masked = *req.Masked
	}
	if err := s.store.CreateOrReplaceSecret(req.Name, stored, principal, req.Repo, masked, time.Now().UTC()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info("secret written", "name", req.Name, "principal", principal,
		"repo", req.Repo, "encrypted", s.secretsCipher != nil, "masked", masked)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	repo, ok := s.secretRepoForReader(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("secrets: resolve caller repository"))
		return
	}
	sec, err := s.store.GetSecretForRepo(name, repo)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	plain := sec.Value
	if s.secretsCipher != nil {
		opened, oerr := s.secretsCipher.Open(plain)
		if oerr != nil {
			writeError(w, http.StatusInternalServerError, oerr)
			return
		}
		plain = opened
	} else if secrets.IsEncrypted(plain) {
		writeError(w, http.StatusInternalServerError, errors.New("secrets cipher: encrypted value but no key configured"))
		return
	}
	writeJSON(w, http.StatusOK, secretJSON{
		Name:      sec.Name,
		Value:     plain,
		Principal: sec.Principal,
		Repo:      sec.Repo,
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
		out = append(out, secretJSON{
			Name:      sec.Name,
			Principal: sec.Principal,
			Repo:      sec.Repo,
			Masked:    sec.Masked,
			CreatedAt: sec.CreatedAt.Unix(),
			UpdatedAt: sec.UpdatedAt.Unix(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"secrets": out})
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.DeleteSecret(name, r.URL.Query().Get("repo")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// safety: a non-admin reader never names its own repository; the live claim it holds names it.
func (s *Server) secretRepoForReader(r *http.Request) (string, bool) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p.HasScope(ScopeAdmin) {
		return r.URL.Query().Get("repo"), true
	}
	repo, err := s.store.RepoForPrincipalClaim(r.Context(), p.Name, time.Now())
	if err != nil {
		s.logger.Error("secret read: resolve claim repository", "principal", p.Name, "err", err)
		return "", false
	}
	return repo, true
}
