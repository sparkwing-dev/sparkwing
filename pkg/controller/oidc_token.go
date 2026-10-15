package controller

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/oidcissuer"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	oidcTokensPerRun = 30
	oidcTokenRefill  = 5 * time.Minute
	githubEventPush  = "push"
)

type oidcState struct {
	issuer  *oidcissuer.Issuer
	limit   *ratelimit.Limiter
	resolve func(context.Context, store.Team) (OIDCReader, error)
}

// OIDCTokenRequest selects the relying party for the authenticated run.
type OIDCTokenRequest struct {
	Audience string `json:"audience"`
}

// OIDCTokenResponse carries a signed ID token for one run.
type OIDCTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// WithOIDCIssuer configures the signing key and per-claim issuance budget.
func (s *Server) WithOIDCIssuer(iss *oidcissuer.Issuer) *Server {
	s.oidc.issuer, s.oidc.limit = iss, ratelimit.New(oidcTokensPerRun, oidcTokenRefill)
	return s
}

// OIDCReader rechecks claim authority and reads sealed identity under one store clock.
type OIDCReader interface {
	Team() store.Team
	ReadOIDCFacts(context.Context, capability.Authorized) (capability.OIDCFacts, error)
}

// WithOIDCReader supplies tenant OIDC facts before Handler is constructed.
func (s *Server) WithOIDCReader(resolve func(context.Context, store.Team) (OIDCReader, error)) *Server {
	s.oidc.resolve = resolve
	return s
}

func (s *Server) oidcReader(ctx context.Context, team string) (OIDCReader, error) {
	var reader OIDCReader
	var err error
	if s.oidc.resolve != nil {
		reader, err = s.oidc.resolve(ctx, store.Team(team))
	} else if s.store != nil {
		var tenant *store.Tenant
		tenant, err = s.store.ForTeam(ctx, store.Team(team))
		reader, _ = any(tenant).(OIDCReader)
	}
	if err != nil {
		return nil, engineLookupError(err)
	}
	if reader == nil || reader.Team() != store.Team(team) {
		return nil, missingEndpointPolicy()
	}
	return reader, nil
}

func (s *Server) handleOIDCDiscovery(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	if s.oidc.issuer == nil {
		return &apierr.Error{Code: apierr.NotFound, Message: errOIDCOff.Error()}
	}
	s.oidc.issuer.ServeDiscovery(w, r)
	return nil
}

func (s *Server) handleOIDCKeys(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	if s.oidc.issuer == nil {
		return &apierr.Error{Code: apierr.NotFound, Message: errOIDCOff.Error()}
	}
	s.oidc.issuer.ServeJWKS(w, r)
	return nil
}

var errOIDCOff = errors.New("this controller holds no OIDC signing key, so it issues no ID tokens")

func (s *Server) handleOIDCToken(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if s.oidc.issuer == nil {
		return &apierr.Error{Code: apierr.NotFound, Message: errOIDCOff.Error()}
	}
	var body OIDCTokenRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if oidcissuer.ValidateAudience(body.Audience) != nil {
		return invalidEngineBody("invalid OIDC audience")
	}
	reader, err := s.oidcReader(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	facts, err := reader.ReadOIDCFacts(r.Context(), a)
	if err != nil {
		return engineLookupError(err)
	}
	if facts.Team != a.Target.Team || facts.RunID != a.Target.RunID || facts.Now.IsZero() || !facts.NotAfter.After(facts.Now) {
		return errors.New("invalid persisted OIDC identity")
	}
	ceiling := facts.NotAfter
	for _, end := range []*time.Time{a.Principal.Capability.ExpiresAt, a.Principal.Capability.RevokedAt} {
		if end != nil && end.Before(ceiling) {
			ceiling = *end
		}
	}
	if end := a.Binding.Deadline; end != nil && end.Before(ceiling) {
		ceiling = *end
	}
	claims := oidcissuer.Claims{Audience: body.Audience, Team: facts.Team, RunID: facts.RunID, Pipeline: facts.Pipeline,
		Trigger: facts.Trigger, RunnerKind: facts.RunnerKind, Ref: facts.Ref, SHA: facts.SHA, Repository: facts.Repository, RepositoryID: facts.RepositoryID}
	if _, err := claims.Subject(); err != nil {
		return &apierr.Error{Code: apierr.BadRequest, Message: "run identity cannot form an OIDC subject"}
	}
	key := a.Principal.Authority.ID + "\x00" + a.Target.RunID
	if ok, wait := s.oidc.limit.AllowWithRetry(key, facts.Now); !ok {
		setRetryAfter(w, wait)
		return &apierr.Error{Code: apierr.RateLimited, Message: "OIDC issuance budget exhausted"}
	}
	if err := r.Context().Err(); err != nil {
		return err
	}
	token, expires, err := s.oidc.issuer.Mint(claims, facts.Now, ceiling)
	if err != nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "OIDC issuance unavailable"}
	}
	writeJSON(w, http.StatusOK, OIDCTokenResponse{Token: token, ExpiresAt: expires.UTC()})
	return nil
}
