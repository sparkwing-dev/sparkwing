package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// The cache scopes every key by what the grant carries, so the controller
// signs in the run's own repository and refs, read from its trigger. Only a
// signed webhook's run writes under its ref; a submitted run writes beside it.
// A signed binary download looks in the run's own scope first.
func TestCacheGrantCarriesTheRunsRepositoryAndRefs(t *testing.T) {
	s, fixtureGrant, head := downloadFixture(t)
	issued, err := authwire.VerifyCacheGrant("grant-key", fixtureGrant, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	mint := func(source string, env map[string]string) authwire.CacheGrant {
		t.Helper()
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.DB().ExecContext(t.Context(),
			`UPDATE triggers SET git_branch = 'ignored', repo = 'acme/app', trigger_source = ?, trigger_env = ? WHERE id = 'run-1'`,
			source, raw); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run-1/cache-grant", nil)
		req.SetPathValue("id", "run-1")
		req.Header.Set(store.TriggerGenerationHeader, "1")
		req = req.WithContext(contextWithPrincipal(req.Context(), &Principal{
			Name: issued.Claim.Principal, TokenPrefix: issued.Claim.TokenPrefix, Kind: store.TokenKindRunner, Team: "team-a",
		}))
		rec := httptest.NewRecorder()
		s.handleRunCacheGrant(teamA).ServeHTTP(rec, req)
		body := decodeGrant(t, rec)
		grant, err := authwire.VerifyCacheGrant("grant-key", body.Grant, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if grant.Scope == nil || grant.Scope.Repo != "name:acme/app" {
			t.Fatalf("grant scope = %+v, want repository name:acme/app", grant.Scope)
		}
		if rec, _ := callDownload(t, s, body.Grant, "bins/abc", false); rec.Code != http.StatusOK {
			t.Fatalf("download = %d: %s", rec.Code, rec.Body.String())
		}
		return grant
	}
	refs := map[string]string{"GITHUB_REF": "refs/heads/feature", sparkwing.EnvPRBaseRef: "main", EnvDefaultBranch: "trunk"}

	submitted := mint("api", refs)
	want := []string{"manual:refs/heads/feature", "refs/heads/feature", "refs/heads/main", "refs/heads/trunk"}
	if !slices.Equal(submitted.Scope.Refs, want) {
		t.Fatalf("submitted run's refs = %v, want %v", submitted.Scope.Refs, want)
	}
	if len(head.keys) == 0 || head.keys[0] != "cache/teams/team-a/"+submitted.ScopePrefixes()[0]+"bins/abc" {
		t.Fatalf("download looked up %v first, want the run's own scope", head.keys)
	}

	refs[sparkwing.EnvGitHubEventName] = sparkwing.EventPullRequest
	webhook := mint(oidcWebhookSource, refs)
	if want := want[1:]; !slices.Equal(webhook.Scope.Refs, want) {
		t.Fatalf("webhook run's refs = %v, want %v", webhook.Scope.Refs, want)
	}
	if webhook.ScopePrefixes()[0] != submitted.ScopePrefixes()[1] {
		t.Fatal("a submitted run does not read what the webhook's run of its ref wrote")
	}
}
