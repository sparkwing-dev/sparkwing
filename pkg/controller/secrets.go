package controller

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
	if s.secretsCipher == nil {
		writeError(w, http.StatusServiceUnavailable, errNoSecretsCipher)
		return
	}
	binding := secretBinding{Team: tn.Team(), Name: req.Name, Scope: req.Pipeline, Shared: req.Shared, Masked: masked}
	stored, sErr := sealSecret(s.secretsCipher, binding, req.Value)
	if errors.Is(sErr, secrets.ErrKeyRefused) {
		writeError(w, http.StatusConflict, sErr)
		return
	}
	if sErr != nil {
		writeError(w, http.StatusInternalServerError, sErr)
		return
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
	noteAuditTarget(r.Context(), "secret_name", req.Name)
	s.logger.Info("secret written", "name", req.Name, "principal", principal,
		"pipeline", req.Pipeline, "masked", masked, "shared", req.Shared)
	w.WriteHeader(http.StatusNoContent)
}

// safety: a server built without a cipher would otherwise store or serve
// plaintext, so it refuses secret reads and writes outright.
var errNoSecretsCipher = errors.New("secrets cipher: this controller has no secrets key, so it stores and serves no secret")

// safety: the name rule guards new rows only, so a row written under an older rule can still be rotated.
func validateSecretName(tn *store.Tenant, name, pipeline string) error {
	if row, err := tn.GetSecretRow(name, pipeline); err == nil && row != nil {
		return nil
	}
	return secrets.ValidateName(name)
}

func (s *Server) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	sec, tn, ok := s.readSecretForCaller(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	noteAuditTarget(r.Context(), "secret_name", sec.Name)
	if p, authed := PrincipalFromContext(r.Context()); authed && sec.Masked && !maskedValueReadable(p) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code:      "write_only",
			Principal: p.label(),
			Message:   "a masked secret's value is returned only to the operator's bearer token or to a claim that declares it",
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

// safety: a masked value leaves the controller only for the operator's admin bearer or a claim's declared read;
// a session holds no run and would only display it, and a team owner manages the row without reading it.
func maskedValueReadable(p *Principal) bool {
	return p.session == "" && p.HasScope(ScopeAdmin)
}

// safety: responses carry credentials or tenant data that browser disk caches
// and intermediaries must not keep.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) openStoredSecret(team store.Team, sec *store.Secret) (string, error) {
	if s.secretsCipher == nil {
		return "", errNoSecretsCipher
	}
	opened, err := openSecret(s.secretsCipher, bindingForRow(team, sec), sec.Value)
	if err != nil {
		s.logger.Error("secret read: open envelope", "team", team, "name", sec.Name, "pipeline", sec.Pipeline, "err", err)
		// safety: the cipher's own text names the row's storage state, which a reader that cannot open it may not learn.
		return "", errors.New("secrets cipher: stored value did not open")
	}
	return opened, nil
}

// safety: a node reads its run's secrets with its claim token, under the plan's
// declaration and with an audit event, so a bearer reads by name only as the
// operator or a team's owner, in its own team; any other bearer is refused
// rather than resolved through a run it holds a claim in.
func (s *Server) readSecretForCaller(w http.ResponseWriter, r *http.Request, name string) (*store.Secret, *store.Tenant, bool) {
	q := r.URL.Query()
	p, authed := PrincipalFromContext(r.Context())
	if authed && !p.HasScope(ScopeAdmin) && !p.HasScope(ScopeTeamAdmin) {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code:      "claim_required",
			Principal: p.label(),
			Message:   "a node reads secrets with its claim token; a read by name needs an operator or team-admin credential",
		})
		return nil, nil, false
	}
	tn, ok := s.requestTenant(w, r)
	if !ok {
		return nil, nil, false
	}
	pipeline := q.Get("pipeline")
	if runID := q.Get("run"); pipeline == "" && runID != "" {
		if run, rerr := tn.GetRun(r.Context(), runID); rerr == nil && run != nil {
			pipeline = run.Pipeline
		}
	}
	sec, err := tn.GetSecretForPipeline(name, pipeline)
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

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tn, err := s.credentialInventory(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	secs, err := tn.ListSecrets()
	if err != nil {
		return engineLookupError(err)
	}
	out := make([]secretJSON, 0, len(secs))
	for _, sec := range secs {
		var value string
		if !sec.Masked {
			if s.secretsCipher == nil {
				return &apierr.Error{Code: apierr.Unavailable, Message: "secret decryption is unavailable"}
			}
			value, err = openSecret(s.secretsCipher, bindingForRow(tn.Team(), &sec), sec.Value)
			if err != nil {
				return errors.New("stored secret did not open")
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
	return nil
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
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
	noteAuditTarget(r.Context(), "secret_name", name)
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
	Rotated        int                 `json:"rotated"`
	Skipped        []secretsRotateSkip `json:"skipped"`
	GitCredentials int                 `json:"git_credentials"`
}

type secretsRotateSkip struct {
	Name     string `json:"name"`
	Pipeline string `json:"pipeline,omitempty"`
}
