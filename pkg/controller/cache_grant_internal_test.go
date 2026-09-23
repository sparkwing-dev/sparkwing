package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func cacheGrantServer(t *testing.T, cacheToken, grantKey string) *Server {
	t.Helper()
	t.Setenv("SPARKWING_CACHE_TOKEN", "")
	t.Setenv(authwire.CacheGrantKeyEnv, grantKey)
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, nil).WithCacheCredentials("http://cache.invalid", cacheToken)
}

func teamA(*http.Request) (store.Team, error) { return "team-a", nil }

func mintAs(t *testing.T, s *Server, p *Principal, teamOf func(*http.Request) (store.Team, error)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run-1/cache-grant", nil)
	req.SetPathValue("id", "run-1")
	if p != nil {
		req = req.WithContext(contextWithPrincipal(req.Context(), p))
	}
	rec := httptest.NewRecorder()
	s.handleRunCacheGrant(teamOf).ServeHTTP(rec, req)
	return rec
}

func mintFor(t *testing.T, s *Server, teamOf func(*http.Request) (store.Team, error)) *httptest.ResponseRecorder {
	t.Helper()
	return mintAs(t, s, nil, teamOf)
}

func decodeGrant(t *testing.T, rec *httptest.ResponseRecorder) CacheGrantResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("mint = %d: %s", rec.Code, rec.Body.String())
	}
	var body CacheGrantResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestCacheGrantNamesTheTeamTheResolverAnswers(t *testing.T) {
	s := cacheGrantServer(t, "operator-token", "grant-key")
	body := decodeGrant(t, mintFor(t, s, teamA))
	g, err := authwire.VerifyCacheGrant("grant-key", body.Grant, time.Now())
	if err != nil || g.Team != "team-a" || g.Run != "run-1" {
		t.Fatalf("minted grant = %+v, %v", g, err)
	}
}

// The grant key is the only thing that signs: a grant the controller mints
// does not verify under the cache's operator token.
func TestCacheGrantIsSignedWithTheGrantKeyNotTheCacheToken(t *testing.T) {
	s := cacheGrantServer(t, "operator-token", "grant-key")
	body := decodeGrant(t, mintFor(t, s, teamA))
	if _, err := authwire.VerifyCacheGrant("operator-token", body.Grant, time.Now()); err == nil {
		t.Fatal("a minted grant verified under the cache's operator token")
	}
}

func TestCacheGrantRefusesACallerWithNoTeam(t *testing.T) {
	s := cacheGrantServer(t, "operator-token", "grant-key")
	rec := mintFor(t, s, func(*http.Request) (store.Team, error) { return "", errors.New("no team") })
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mint with no team = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

func TestCacheGrantNeedsAGrantKeyToSignWith(t *testing.T) {
	s := cacheGrantServer(t, "operator-token", "")
	rec := mintFor(t, s, teamA)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("mint with no grant key = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestCacheGrantRefusesAGrantKeyThatIsTheCacheToken(t *testing.T) {
	s := cacheGrantServer(t, "same-secret", "same-secret")
	if rec := mintFor(t, s, teamA); rec.Code/100 == 2 {
		t.Fatalf("mint with the grant key equal to the cache token = %d, want a refusal", rec.Code)
	}
}

// A GitHub Actions job's credential is confined to one repository, and a
// grant opens its whole team's cache tree, so it gets none.
func TestCacheGrantRefusesAGitHubRunnerCredential(t *testing.T) {
	s := cacheGrantServer(t, "operator-token", "grant-key")
	p := &Principal{
		Name: store.GitHubRunnerPrincipalPrefix + "42:acme/app", Kind: store.TokenKindRunner,
		Team: "team-a", Scopes: runnerTokenScopes, Expires: time.Now().Add(time.Hour),
	}
	if rec := mintAs(t, s, p, teamA); rec.Code != http.StatusForbidden {
		t.Fatalf("mint for a GitHub Actions credential = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// A grant cannot outlive the credential that asked for it.
func TestCacheGrantEndsWhenTheCredentialDoes(t *testing.T) {
	s := cacheGrantServer(t, "operator-token", "grant-key")
	expires := time.Now().Add(10 * time.Minute)
	p := &Principal{Name: "pool", Kind: store.TokenKindRunner, Team: "team-a", Scopes: runnerTokenScopes, Expires: expires}
	body := decodeGrant(t, mintAs(t, s, p, teamA))
	if body.ExpiresAt.After(expires) {
		t.Fatalf("grant expires %s, after the credential's %s", body.ExpiresAt, expires)
	}
	if _, err := authwire.VerifyCacheGrant("grant-key", body.Grant, expires.Add(time.Second)); err == nil {
		t.Fatal("the grant still verifies after the credential expired")
	}
	expired := &Principal{
		Name: "pool", Kind: store.TokenKindRunner, Team: "team-a", Scopes: runnerTokenScopes,
		Expires: time.Now().Add(-time.Second),
	}
	if rec := mintAs(t, s, expired, teamA); rec.Code/100 == 2 {
		t.Fatalf("mint for an expired credential = %d, want a refusal", rec.Code)
	}
}
