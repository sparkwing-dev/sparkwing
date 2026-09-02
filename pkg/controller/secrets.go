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
	// safety: an unscoped secret answers a run only when an admin marked it shared.
	Shared bool `json:"shared,omitempty"`
}

type secretJSON struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	Principal string `json:"principal"`
	Repo      string `json:"repo,omitempty"`
	Masked    bool   `json:"masked"`
	Shared    bool   `json:"shared,omitempty"`
	Bound     bool   `json:"bound"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func (s *Server) handleCreateSecret(w http.ResponseWriter, r *http.Request) {
	var req secretSetReq
	if err := decodeJSONLimit(r, &req, maxSecretJSONBody); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.validateSecretName(req.Name, req.Repo); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	principal := "anonymous"
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil {
		principal = p.Name
	}
	masked := true
	if req.Masked != nil {
		masked = *req.Masked
	}
	stored := req.Value
	if s.secretsCipher != nil {
		binding := secretBinding{Name: req.Name, Repo: req.Repo, Shared: req.Shared, Masked: masked}
		sealed, sErr := sealSecret(s.secretsCipher, binding, req.Value)
		if sErr != nil {
			writeError(w, http.StatusInternalServerError, sErr)
			return
		}
		stored = sealed
	}
	if err := s.store.CreateOrReplaceSecret(store.Secret{
		Name:      req.Name,
		Value:     stored,
		Principal: principal,
		Repo:      req.Repo,
		Masked:    masked,
		Shared:    req.Shared,
	}, time.Now().UTC()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info("secret written", "name", req.Name, "principal", principal,
		"repo", req.Repo, "encrypted", s.secretsCipher != nil, "masked", masked, "shared", req.Shared)
	w.WriteHeader(http.StatusNoContent)
}

// safety: the name rule guards new rows only, so a row written under an older rule can still be rotated.
func (s *Server) validateSecretName(name, repo string) error {
	if row, err := s.store.GetSecretRow(name, repo); err == nil && row != nil {
		return nil
	}
	return secrets.ValidateName(name)
}

func (s *Server) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	sec, ok := s.readSecretForCaller(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	plain := sec.Value
	bound := secrets.IsBound(sec.Value)
	if s.secretsCipher != nil {
		opened, oerr := openSecret(s.secretsCipher, bindingForRow(sec), plain)
		if oerr != nil {
			s.logger.Error("secret read: open envelope", "name", sec.Name, "repo", sec.Repo, "err", oerr)
			// safety: the cipher's own text names the row's storage state, which a reader that cannot open it may not learn.
			writeError(w, http.StatusInternalServerError, errors.New("secrets cipher: stored value did not open"))
			return
		}
		plain = opened
		if !bound {
			bound = s.rebindSecret(sec, plain)
		}
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
		Shared:    sec.Shared,
		Bound:     bound,
		CreatedAt: sec.CreatedAt.Unix(),
		UpdatedAt: sec.UpdatedAt.Unix(),
	})
}

// safety: an envelope written before binding is substitutable, so a successful read reseals it in place.
func (s *Server) rebindSecret(sec *store.Secret, plain string) bool {
	if _, ok := s.secretsCipher.(BoundCipher); !ok {
		return false
	}
	sealed, err := sealSecret(s.secretsCipher, bindingForRow(sec), plain)
	if err != nil {
		s.logger.Error("secret rebind: seal", "name", sec.Name, "repo", sec.Repo, "err", err)
		return false
	}
	row := *sec
	row.Value = sealed
	if err := s.store.CreateOrReplaceSecret(row, sec.UpdatedAt); err != nil {
		s.logger.Error("secret rebind: store", "name", sec.Name, "repo", sec.Repo, "err", err)
		return false
	}
	s.logger.Info("secret envelope rebound", "name", sec.Name, "repo", sec.Repo)
	return true
}

// safety: a non-admin reader never names its own repository; the live claim it holds names it.
func (s *Server) readSecretForCaller(w http.ResponseWriter, r *http.Request, name string) (*store.Secret, bool) {
	q := r.URL.Query()
	runID := q.Get("run")
	p, authed := PrincipalFromContext(r.Context())
	if !authed || p.HasScope(ScopeAdmin) {
		repo := q.Get("repo")
		if repo == "" && runID != "" {
			if run, rerr := s.store.GetRun(r.Context(), runID); rerr == nil && run != nil {
				repo = run.Repo
			}
		}
		sec, err := s.store.GetSecretForRepo(name, repo)
		return sec, reportSecretRead(w, sec, err)
	}
	repo, refused := s.repoForClaimingReader(r, runID)
	if refused != "" {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code:      "claim_required",
			Principal: p.label(),
			Message:   refused,
		})
		return nil, false
	}
	sec, err := s.store.GetSecretForRun(name, repo)
	return sec, reportSecretRead(w, sec, err)
}

func reportSecretRead(w http.ResponseWriter, sec *store.Secret, err error) bool {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
		return false
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
		return false
	case sec == nil:
		writeError(w, http.StatusNotFound, store.ErrNotFound)
		return false
	}
	return true
}

func (s *Server) repoForClaimingReader(r *http.Request, runID string) (repo, refused string) {
	claimant := claimIdentity(r)
	now := time.Now()
	if runID != "" {
		repo, err := s.store.RepoForClaimedRun(r.Context(), runID, claimant, now)
		if errors.Is(err, store.ErrNotFound) {
			return "", "run " + runID + " is not claimed by this principal"
		}
		if err != nil {
			s.logger.Error("secret read: resolve claimed run", "run_id", runID, "err", err)
			return "", "resolve the caller's claimed repository"
		}
		return repo, ""
	}
	repos, err := s.store.ReposForClaimant(r.Context(), claimant, now)
	if err != nil {
		s.logger.Error("secret read: resolve claim repository", "err", err)
		return "", "resolve the caller's claimed repository"
	}
	switch len(repos) {
	case 0:
		return "", "this principal holds no live claim, so no repository names its secrets"
	case 1:
		return repos[0], ""
	default:
		return "", "this principal holds claims in more than one repository; name the run with ?run=<id>"
	}
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
			Shared:    sec.Shared,
			Bound:     secrets.IsBound(sec.Value),
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
