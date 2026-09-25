package controller

import (
	"errors"
	"fmt"
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
	noStoreSecrets(w)
	var req secretSetReq
	if err := decodeJSONLimit(r, &req, maxSecretJSONBody); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tn, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	if err := validateSecretName(tn, req.Name, req.Pipeline); err != nil {
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
	// safety: the envelope is sealed to the team the row is written into, so
	// one tenant serves both and they cannot drift apart.
	stored := req.Value
	if s.secretsCipher != nil {
		binding := secretBinding{Team: tn.Team(), Name: req.Name, Scope: req.Pipeline, Shared: req.Shared, Masked: masked}
		sealed, sErr := sealSecret(s.secretsCipher, binding, req.Value)
		if sErr != nil {
			writeError(w, http.StatusInternalServerError, sErr)
			return
		}
		stored = sealed
	}
	if err := tn.CreateOrReplaceSecret(store.Secret{
		Name:      req.Name,
		Value:     stored,
		Principal: principal,
		Pipeline:  req.Pipeline,
		Masked:    masked,
		Shared:    req.Shared,
	}, time.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrSecretLimit) {
			writeError(w, http.StatusRequestEntityTooLarge, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info("secret written", "name", req.Name, "principal", principal,
		"pipeline", req.Pipeline, "encrypted", s.secretsCipher != nil, "masked", masked, "shared", req.Shared)
	w.WriteHeader(http.StatusNoContent)
}

// safety: the name rule guards new rows only, so a row written under an older rule can still be rotated.
func validateSecretName(tn *store.Tenant, name, pipeline string) error {
	if row, err := tn.GetSecretRow(name, pipeline); err == nil && row != nil {
		return nil
	}
	return secrets.ValidateName(name)
}

func (s *Server) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	noStoreSecrets(w)
	sec, tn, ok := s.readSecretForCaller(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	if p, authed := PrincipalFromContext(r.Context()); authed && sec.Masked && !maskedValueReadable(p) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code:      "write_only",
			Principal: p.label(),
			Message:   "a masked secret's value is returned only to the operator's bearer token or to a runner's claimed run",
		})
		return
	}
	plain, err := s.openStoredSecret(tn.Team(), sec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, secretJSON{
		Name:      sec.Name,
		Value:     plain,
		Principal: sec.Principal,
		Pipeline:  sec.Pipeline,
		Masked:    sec.Masked,
		Shared:    sec.Shared,
		Bound:     secrets.IsBound(sec.Value),
		CreatedAt: sec.CreatedAt.Unix(),
		UpdatedAt: sec.UpdatedAt.Unix(),
	})
}

// safety: a masked value leaves the controller only for the operator's admin
// bearer or a runner's claim-bound read; a session holds no run and would only
// display it, and a team owner manages the row without reading it back.
func maskedValueReadable(p *Principal) bool {
	if p.session != "" {
		return false
	}
	if p.HasScope(ScopeAdmin) {
		return true
	}
	// safety: readSecretForCaller resolves every other caller through its claimed run.
	return !p.HasScope(ScopeTeamAdmin)
}

func noStoreSecrets(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func (s *Server) openStoredSecret(team store.Team, sec *store.Secret) (string, error) {
	if s.secretsCipher == nil {
		if secrets.IsEncrypted(sec.Value) {
			return "", errors.New("secrets cipher: encrypted value but no key configured")
		}
		return sec.Value, nil
	}
	opened, err := openSecret(s.secretsCipher, bindingForRow(team, sec), sec.Value)
	if err != nil {
		s.logger.Error("secret read: open envelope", "team", team, "name", sec.Name, "pipeline", sec.Pipeline, "err", err)
		// safety: the cipher's own text names the row's storage state, which a reader that cannot open it may not learn.
		return "", errors.New("secrets cipher: stored value did not open")
	}
	return opened, nil
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
	// safety: the operator and a team's owner read by name in their own team;
	// a run they name is looked up there too, so another team's run names no
	// pipeline and its secrets stay unread.
	if !authed || p.HasScope(ScopeAdmin) || p.HasScope(ScopeTeamAdmin) {
		tn, ok := s.requestTenant(w, r)
		if !ok {
			return nil, nil, false
		}
		pipeline := q.Get("pipeline")
		if pipeline == "" && runID != "" {
			if run, rerr := tn.GetRun(r.Context(), runID); rerr == nil && run != nil {
				pipeline = run.Pipeline
			}
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
	noStoreSecrets(w)
	tn, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	secs, err := tn.ListSecrets()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]secretJSON, 0, len(secs))
	for _, sec := range secs {
		// safety: an unmasked row is plain config every member may read, while
		// a masked row's value never appears in a list, whoever asks. A row
		// that does not open lists without its value, so one bad row cannot
		// hide the others, and openStoredSecret has logged it.
		var value string
		if !sec.Masked {
			if opened, oerr := s.openStoredSecret(tn.Team(), &sec); oerr == nil {
				value = opened
			}
		}
		out = append(out, secretJSON{
			Name:      sec.Name,
			Value:     value,
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
	noStoreSecrets(w)
	name := r.PathValue("name")
	tn, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	if err := tn.DeleteSecret(name, r.URL.Query().Get("pipeline")); err != nil {
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
	noStoreSecrets(w)
	if s.secretsCipher == nil {
		writeError(w, http.StatusBadRequest,
			errors.New("secrets cipher: no key configured, so there is nothing to rotate to"))
		return
	}
	skipped := []secretsRotateSkip{}
	total, err := s.store.RotateSecretValues(r.Context(), func(team store.Team, sec store.Secret) (string, error) {
		sealed, rerr := s.resealStoredSecret(team, sec)
		if rerr != nil {
			// safety: one unreadable row must not cost every other row its rotation, so it keeps its bytes.
			s.logger.Error("secret rotate: open envelope", "team", team, "name", sec.Name, "pipeline", sec.Pipeline, "err", rerr)
			skipped = append(skipped, secretsRotateSkip{Name: sec.Name, Pipeline: sec.Pipeline})
			return sec.Value, nil
		}
		return sealed, nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// safety: a git credential is always sealed bound to its team and host,
	// so one that does not open fails the rotation rather than being kept
	// under a key the operator is about to drop.
	gitCreds, err := s.store.RotateGitCredentialSecrets(r.Context(), func(c store.GitCredential) (string, error) {
		binding := gitCredentialBinding(c.Team, c.Host)
		plain, oerr := openSecret(s.secretsCipher, binding, c.Secret)
		if oerr != nil {
			return "", fmt.Errorf("git credential for %s in team %s: %w", c.Host, c.Team, oerr)
		}
		return sealSecret(s.secretsCipher, binding, plain)
	})
	if err != nil {
		s.writeInternalError(w, r, "rotate git credentials", err)
		return
	}
	principal := "anonymous"
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil {
		principal = p.Name
	}
	s.logger.Info("secrets rotated", "count", total-len(skipped), "skipped", len(skipped),
		"git_credentials", gitCreds, "principal", principal)
	writeJSON(w, http.StatusOK, secretsRotateResponse{Rotated: total - len(skipped), Skipped: skipped, GitCredentials: gitCreds})
}

type secretsRotateResponse struct {
	Rotated int                 `json:"rotated"`
	Skipped []secretsRotateSkip `json:"skipped"`
	// GitCredentials is how many team git credentials were resealed.
	GitCredentials int `json:"git_credentials"`
}

type secretsRotateSkip struct {
	Name     string `json:"name"`
	Pipeline string `json:"pipeline,omitempty"`
}
