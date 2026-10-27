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
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const (
	oidcTokensPerRun = 30
	oidcTokenRefill  = 5 * time.Minute
	githubEventPush  = "push"
)

type oidcState struct {
	issuer *oidcissuer.Issuer
	limit  *ratelimit.Limiter
}

// oidcTokenRequest selects the relying party for the authenticated run.
type oidcTokenRequest struct {
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

// OIDCMinter rechecks claim authority, signs and audits one token in a single store transaction.
type OIDCMinter interface {
	Team() store.Team
	MintOIDCToken(context.Context, capability.Authorized, string, func(capability.OIDCFacts) (store.OIDCMint, error)) (store.OIDCMint, error)
}

func (s *Server) oidcMinter(ctx context.Context, team string) (OIDCMinter, error) {
	return tenantPort[OIDCMinter](ctx, s, store.Team(team))
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
	var body oidcTokenRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if oidcissuer.ValidateAudience(body.Audience) != nil {
		return invalidEngineBody("invalid OIDC audience")
	}
	minter, err := s.oidcMinter(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	var limited time.Duration
	minted, err := minter.MintOIDCToken(r.Context(), a, body.Audience, func(facts capability.OIDCFacts) (store.OIDCMint, error) {
		if facts.Team != a.Target.Team || facts.RunID != a.Target.RunID || facts.Now.IsZero() || !facts.NotAfter.After(facts.Now) {
			return store.OIDCMint{}, errors.New("invalid persisted OIDC identity")
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
		claims := oidcissuer.Claims{
			Audience: body.Audience, Team: facts.Team, RunID: facts.RunID, Pipeline: facts.Pipeline,
			Trigger: facts.Trigger, RunnerKind: facts.RunnerKind, RunnerEnvironment: facts.RunnerEnvironment, Ref: facts.Ref, SHA: facts.SHA, Repository: facts.Repository, RepositoryID: facts.RepositoryID,
		}
		if _, err := claims.Subject(); err != nil {
			return store.OIDCMint{}, &apierr.Error{Code: apierr.BadRequest, Message: "run identity cannot form an OIDC subject"}
		}
		key := a.Principal.Authority.ID + "\x00" + a.Target.RunID
		if ok, wait := s.oidc.limit.AllowWithRetry(key, facts.Now); !ok {
			limited = wait
			return store.OIDCMint{}, &apierr.Error{Code: apierr.RateLimited, Message: "OIDC issuance budget exhausted"}
		}
		if err := r.Context().Err(); err != nil {
			return store.OIDCMint{}, err
		}
		token, err := s.oidc.issuer.Mint(claims, facts.Now, ceiling)
		if err != nil {
			return store.OIDCMint{}, errors.Join(&apierr.Error{Code: apierr.Unavailable, Message: "OIDC issuance unavailable"}, err)
		}
		return store.OIDCMint{Token: token.Value, ID: token.ID, Subject: token.Subject, ExpiresAt: token.ExpiresAt}, nil
	})
	if limited > 0 {
		setRetryAfter(w, limited)
	}
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, OIDCTokenResponse{Token: minted.Token, ExpiresAt: minted.ExpiresAt.UTC()})
	return nil
}

var _ OIDCMinter = (*store.Tenant)(nil)
