// Package githuboidctest runs a local stand-in for GitHub's Actions token
// issuer, so a suite can mint the ID tokens a workflow job would present.
package githuboidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githuboidc"
	"github.com/sparkwing-dev/sparkwing/internal/jwks/jwkstest"
)

// Issuer is the fake. Its URL is what tokens name in iss.
type Issuer struct {
	URL    string
	signer *jwkstest.Signer
	t      testing.TB
}

// Job is the workflow run a token speaks for.
type Job struct {
	Repository        string
	RepositoryID      int64
	RepositoryOwnerID int64
	Ref               string
	RunID             string
}

// New starts an issuer and stops it when t ends.
func New(t testing.TB) *Issuer {
	t.Helper()
	iss := &Issuer{signer: jwkstest.NewSigner(t, "gh-test-key"), t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/jwks", iss.signer.ServeKeys)
	srv := httptest.NewServer(mux)
	iss.URL = srv.URL
	t.Cleanup(srv.Close)
	return iss
}

// Config returns a verifier configuration that trusts this issuer.
func (i *Issuer) Config(audience string) githuboidc.Config {
	return githuboidc.Config{Audience: audience, Issuer: i.URL, JWKSURL: i.URL + "/.well-known/jwks"}
}

// Token mints an ID token for job with audience.
func (i *Issuer) Token(job Job, audience string) string {
	return i.TokenWith(job, audience, nil)
}

// TokenWith is Token with claim overrides, which is how a test builds a token
// with the wrong issuer or expiry. A nil value deletes the claim.
func (i *Issuer) TokenWith(job Job, audience string, override map[string]any) string {
	return i.sign(i.signer.Key, job, audience, override)
}

// TokenSignedBy mints a token signed by a key the issuer does not publish.
func (i *Issuer) TokenSignedBy(key *rsa.PrivateKey, job Job, audience string) string {
	return i.sign(key, job, audience, nil)
}

// ForeignKey returns a key the issuer does not publish.
func (i *Issuer) ForeignKey() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		i.t.Fatal(err)
	}
	return key
}

func (i *Issuer) sign(key *rsa.PrivateKey, job Job, audience string, override map[string]any) string {
	now := time.Now()
	ref := job.Ref
	if ref == "" {
		ref = "refs/heads/main"
	}
	runID := job.RunID
	if runID == "" {
		runID = "1000"
	}
	claims := map[string]any{
		"iss": i.URL, "aud": audience, "sub": "repo:" + job.Repository + ":ref:" + ref,
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"repository":          job.Repository,
		"repository_id":       strconv.FormatInt(job.RepositoryID, 10),
		"repository_owner_id": strconv.FormatInt(job.RepositoryOwnerID, 10),
		"ref":                 ref,
		"workflow_ref":        job.Repository + "/.github/workflows/sparkwing.yaml@" + ref,
		"run_id":              runID,
	}
	for k, v := range override {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	token, err := i.signer.SignWith(key, claims)
	if err != nil {
		i.t.Fatal(err)
	}
	return token
}
