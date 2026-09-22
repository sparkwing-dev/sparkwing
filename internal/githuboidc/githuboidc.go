// Package githuboidc verifies the OpenID Connect ID token a GitHub Actions
// job requests for itself, which proves which repository, workflow and run
// the job belongs to without the job holding a stored secret.
package githuboidc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/jwks"
)

// GitHub's published issuer and key set.
const (
	Issuer  = "https://token.actions.githubusercontent.com"
	JWKSURL = Issuer + "/.well-known/jwks"
)

const clockSkew = time.Minute

// ErrRejected covers every token that does not prove a GitHub Actions job
// asked for it with this verifier's audience.
var ErrRejected = errors.New("githuboidc: token rejected")

// Config names the issuer to trust and the audience a token must carry.
type Config struct {
	// Audience is the aud a token must name. A job sets it when it requests
	// the token, so a token minted for another service is refused.
	Audience string
	Issuer   string
	JWKSURL  string
	HTTP     *http.Client
	Now      func() time.Time
}

// GitHub returns the configuration for GitHub's production issuer.
func GitHub(audience string) Config {
	return Config{Audience: audience, Issuer: Issuer, JWKSURL: JWKSURL}
}

// Claims is what a verified token says about the job that requested it.
type Claims struct {
	// Repository is "owner/name" as it was named when the job ran.
	Repository string
	// RepositoryID and RepositoryOwnerID survive renames and transfers,
	// which is why a binding keys on them rather than on the name.
	RepositoryID      int64
	RepositoryOwnerID int64
	Ref               string
	WorkflowRef       string
	RunID             string
	Subject           string
	ExpiresAt         time.Time
}

// Verifier checks tokens for one audience.
type Verifier struct {
	cfg  Config
	keys *jwks.KeySet
}

// New returns a verifier for cfg.
func New(cfg Config) *Verifier {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Verifier{cfg: cfg, keys: jwks.New(cfg.JWKSURL, cfg.HTTP, cfg.Now)}
}

type tokenClaims struct {
	Iss               string        `json:"iss"`
	Aud               jwks.Audience `json:"aud"`
	Sub               string        `json:"sub"`
	Exp               int64         `json:"exp"`
	Nbf               int64         `json:"nbf"`
	Iat               int64         `json:"iat"`
	Repository        string        `json:"repository"`
	RepositoryID      string        `json:"repository_id"`
	RepositoryOwnerID string        `json:"repository_owner_id"`
	Ref               string        `json:"ref"`
	WorkflowRef       string        `json:"workflow_ref"`
	RunID             string        `json:"run_id"`
}

// Verify checks token's signature, issuer, audience and validity window, and
// returns the job's claims.
func (v *Verifier) Verify(ctx context.Context, token string) (Claims, error) {
	if v.cfg.Audience == "" {
		return Claims{}, errors.New("githuboidc: no audience configured")
	}
	var cl tokenClaims
	if err := v.keys.Verify(ctx, token, &cl); err != nil {
		if errors.Is(err, jwks.ErrRejected) {
			return Claims{}, fmt.Errorf("%w: %w", ErrRejected, err)
		}
		return Claims{}, fmt.Errorf("githuboidc: %w", err)
	}
	now := v.cfg.Now()
	switch {
	case cl.Iss != v.cfg.Issuer:
		return Claims{}, fmt.Errorf("%w: issuer %q", ErrRejected, cl.Iss)
	// safety: GitHub lets a job name any audience, so only an exact match
	// proves the job asked for a token meant for this controller.
	case !slices.Contains(cl.Aud, v.cfg.Audience):
		return Claims{}, fmt.Errorf("%w: token was issued for another audience", ErrRejected)
	case cl.Exp == 0 || !now.Before(time.Unix(cl.Exp, 0).Add(clockSkew)):
		return Claims{}, fmt.Errorf("%w: token expired", ErrRejected)
	case cl.Nbf != 0 && time.Unix(cl.Nbf, 0).After(now.Add(clockSkew)):
		return Claims{}, fmt.Errorf("%w: token not valid yet", ErrRejected)
	case cl.Iat != 0 && time.Unix(cl.Iat, 0).After(now.Add(clockSkew)):
		return Claims{}, fmt.Errorf("%w: token issued in the future", ErrRejected)
	}
	repoID, err := strconv.ParseInt(cl.RepositoryID, 10, 64)
	if err != nil || repoID <= 0 {
		return Claims{}, fmt.Errorf("%w: token names no repository id", ErrRejected)
	}
	ownerID, err := strconv.ParseInt(cl.RepositoryOwnerID, 10, 64)
	if err != nil || ownerID <= 0 {
		return Claims{}, fmt.Errorf("%w: token names no repository owner id", ErrRejected)
	}
	owner, name, ok := strings.Cut(cl.Repository, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return Claims{}, fmt.Errorf("%w: token repository %q is not owner/name", ErrRejected, cl.Repository)
	}
	return Claims{
		Repository: cl.Repository, RepositoryID: repoID, RepositoryOwnerID: ownerID,
		Ref: cl.Ref, WorkflowRef: cl.WorkflowRef, RunID: cl.RunID, Subject: cl.Sub,
		ExpiresAt: time.Unix(cl.Exp, 0),
	}, nil
}
