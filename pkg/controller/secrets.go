package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type secretSetReq struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Pipeline string `json:"pipeline,omitempty"`
	// safety: nil defaults to masked; only an explicit false stores plain config.
	Masked *bool `json:"masked,omitempty"`
	// safety: an unscoped secret answers a run only when an admin marked it shared.
	Shared bool `json:"shared,omitempty"`
}

type secretJSON struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	Principal string `json:"principal"`
	Pipeline  string `json:"pipeline,omitempty"`
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
	if err := s.validateSecretName(req.Name, req.Pipeline); err != nil {
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
		binding := secretBinding{Name: req.Name, Scope: req.Pipeline, Shared: req.Shared, Masked: masked}
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
		Pipeline:  req.Pipeline,
		Masked:    masked,
		Shared:    req.Shared,
	}, time.Now().UTC()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info("secret written", "name", req.Name, "principal", principal,
		"pipeline", req.Pipeline, "encrypted", s.secretsCipher != nil, "masked", masked, "shared", req.Shared)
	w.WriteHeader(http.StatusNoContent)
}

// safety: the name rule guards new rows only, so a row written under an older rule can still be rotated.
func (s *Server) validateSecretName(name, pipeline string) error {
	if row, err := s.store.GetSecretRow(name, pipeline); err == nil && row != nil {
		return nil
	}
	return secrets.ValidateName(name)
}

func (s *Server) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	sec, tn, ok := s.readSecretForCaller(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	plain := sec.Value
	bound := secrets.IsBound(sec.Value)
	if s.secretsCipher != nil {
		opened, oerr := openSecret(s.secretsCipher, bindingForRow(sec), plain)
		if oerr != nil {
			s.logger.Error("secret read: open envelope", "name", sec.Name, "pipeline", sec.Pipeline, "err", oerr)
			// safety: the cipher's own text names the row's storage state, which a reader that cannot open it may not learn.
			writeError(w, http.StatusInternalServerError, errors.New("secrets cipher: stored value did not open"))
			return
		}
		plain = opened
		if !bound {
			bound = s.rebindSecret(tn, sec, plain)
		}
	} else if secrets.IsEncrypted(plain) {
		writeError(w, http.StatusInternalServerError, errors.New("secrets cipher: encrypted value but no key configured"))
		return
	}
	writeJSON(w, http.StatusOK, secretJSON{
		Name:      sec.Name,
		Value:     plain,
		Principal: sec.Principal,
		Pipeline:  sec.Pipeline,
		Masked:    sec.Masked,
		Shared:    sec.Shared,
		Bound:     bound,
		CreatedAt: sec.CreatedAt.Unix(),
		UpdatedAt: sec.UpdatedAt.Unix(),
	})
}

// safety: an envelope written before binding is substitutable, so a successful read reseals it in place,
// in the team it was read from; resealing through the default team would copy one team's value into another's row.
func (s *Server) rebindSecret(tn *store.Tenant, sec *store.Secret, plain string) bool {
	if _, ok := s.secretsCipher.(BoundCipher); !ok {
		return false
	}
	sealed, err := sealSecret(s.secretsCipher, bindingForRow(sec), plain)
	if err != nil {
		s.logger.Error("secret rebind: seal", "name", sec.Name, "pipeline", sec.Pipeline, "err", err)
		return false
	}
	row := *sec
	row.Value = sealed
	if err := tn.CreateOrReplaceSecret(row, sec.UpdatedAt); err != nil {
		s.logger.Error("secret rebind: store", "name", sec.Name, "pipeline", sec.Pipeline, "err", err)
		return false
	}
	s.logger.Info("secret envelope rebound", "name", sec.Name, "pipeline", sec.Pipeline)
	return true
}

// safety: a non-admin reader's standing is the pipeline of a run it holds live
// work in, because a run's repository is a string its submitter typed and
// naming one proves nothing about owning it.
//
// safety: a claimant's read resolves in the claimed run's team, so a runner
// holding one team's run reads that team's rows and never the default team's.
func (s *Server) readSecretForCaller(w http.ResponseWriter, r *http.Request, name string) (*store.Secret, *store.Tenant, bool) {
	q := r.URL.Query()
	runID := q.Get("run")
	p, authed := PrincipalFromContext(r.Context())
	if !authed || p.HasScope(ScopeAdmin) {
		pipeline := q.Get("pipeline")
		if pipeline == "" && runID != "" {
			if run, rerr := s.store.GetRun(r.Context(), runID); rerr == nil && run != nil {
				pipeline = run.Pipeline
			}
		}
		tn, err := s.store.ForTeam(r.Context(), store.DefaultTeam)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return nil, nil, false
		}
		sec, err := tn.GetSecretForPipeline(name, pipeline)
		return sec, tn, reportSecretRead(w, sec, err)
	}
	claimed, refused := s.claimedRunForReader(r, runID)
	if refused != "" {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code:      "claim_required",
			Principal: p.label(),
			Message:   refused,
		})
		return nil, nil, false
	}
	tn, err := s.store.ForTeam(r.Context(), claimed.Team)
	if err != nil {
		s.logger.Error("secret read: resolve the claimed run's team", "team", claimed.Team, "err", err)
		writeError(w, http.StatusInternalServerError, errors.New("resolve the claimed run's team"))
		return nil, nil, false
	}
	sec, err := tn.GetSecretForRun(name, claimed.Pipeline)
	return sec, tn, reportSecretRead(w, sec, err)
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

func (s *Server) claimedRunForReader(r *http.Request, runID string) (claimed store.ClaimedRun, refused string) {
	claimant := claimIdentity(r)
	now := time.Now()
	if runID != "" {
		claimed, err := s.store.ClaimedRunFor(r.Context(), runID, claimant, now)
		if errors.Is(err, store.ErrNotFound) {
			return store.ClaimedRun{}, "run " + runID + " is not claimed by this principal"
		}
		if err != nil {
			s.logger.Error("secret read: resolve claimed run", "run_id", runID, "err", err)
			return store.ClaimedRun{}, "resolve the caller's claimed pipeline"
		}
		return claimed, ""
	}
	runs, err := s.store.ClaimedRunsFor(r.Context(), claimant, now)
	if err != nil {
		s.logger.Error("secret read: resolve claim pipeline", "err", err)
		return store.ClaimedRun{}, "resolve the caller's claimed pipeline"
	}
	switch len(runs) {
	case 0:
		return store.ClaimedRun{}, "this principal holds no live claim, so no pipeline names its secrets"
	case 1:
		return runs[0], ""
	default:
		return store.ClaimedRun{}, "this principal holds claims in more than one pipeline; name the run with ?run=<id>"
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
			Pipeline:  sec.Pipeline,
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
	if err := s.store.DeleteSecret(name, r.URL.Query().Get("pipeline")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// safety: a row held as plaintext and one sealed under the previous key both
// come out under the current key, so dropping the previous key loses nothing.
func (s *Server) handleRotateSecrets(w http.ResponseWriter, r *http.Request) {
	if s.secretsCipher == nil {
		writeError(w, http.StatusBadRequest,
			errors.New("secrets cipher: no key configured, so there is nothing to rotate to"))
		return
	}
	skipped := []secretsRotateSkip{}
	total, err := s.store.RotateSecretValues(r.Context(), func(sec store.Secret) (string, error) {
		binding := bindingForRow(&sec)
		plain := sec.Value
		if secrets.IsEncrypted(plain) {
			opened, oerr := openSecret(s.secretsCipher, binding, plain)
			if oerr != nil {
				// safety: one unreadable row must not cost every other row its rotation, so it keeps its bytes.
				s.logger.Error("secret rotate: open envelope", "name", sec.Name, "pipeline", sec.Pipeline, "err", oerr)
				skipped = append(skipped, secretsRotateSkip{Name: sec.Name, Pipeline: sec.Pipeline})
				return sec.Value, nil
			}
			plain = opened
		}
		return sealSecret(s.secretsCipher, binding, plain)
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	principal := "anonymous"
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil {
		principal = p.Name
	}
	s.logger.Info("secrets rotated", "count", total-len(skipped), "skipped", len(skipped), "principal", principal)
	writeJSON(w, http.StatusOK, secretsRotateResponse{Rotated: total - len(skipped), Skipped: skipped})
}

type secretsRotateResponse struct {
	Rotated int                 `json:"rotated"`
	Skipped []secretsRotateSkip `json:"skipped"`
}

type secretsRotateSkip struct {
	Name     string `json:"name"`
	Pipeline string `json:"pipeline,omitempty"`
}
