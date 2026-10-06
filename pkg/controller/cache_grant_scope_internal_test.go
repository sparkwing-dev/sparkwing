package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
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
			`UPDATE triggers SET git_branch = 'ignored', repo = 'app', repo_url = 'https://GitHub.com/acme/app.git', trigger_source = ?, trigger_env = ? WHERE id = 'run-1'`,
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
		if grant.Scope == nil || grant.Scope.Repo != "github.com/acme/app" {
			t.Fatalf("grant scope = %+v, want repository github.com/acme/app", grant.Scope)
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

	refs[sparkwing.EnvGitHubEventName], refs[sparkwing.EnvPRNumber] = sparkwing.EventPullRequest, "7"
	webhook := mint(oidcWebhookSource, refs)
	if want := want[1:]; !slices.Equal(webhook.Scope.Refs, want) {
		t.Fatalf("webhook run's refs = %v, want %v", webhook.Scope.Refs, want)
	}
	if webhook.ScopePrefixes()[0] != submitted.ScopePrefixes()[1] {
		t.Fatal("a submitted run does not read what the webhook's run of its ref wrote")
	}
}

// A run writes under its real ref when server-held state names that ref and
// commit: a signed push, a schedule that follows the branch tip, or a retry or
// child of one that runs the same ref and commit. Whatever its submitter chose
// writes beside it, and still needs its claim fence to get a grant at all.
func TestCacheGrantWritesUnderTheRealRefOnlyWhenTheServerHoldsIt(t *testing.T) {
	s, fixtureGrant, _ := downloadFixture(t)
	issued, err := authwire.VerifyCacheGrant("grant-key", fixtureGrant, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	team, err := s.store.ForTeam(t.Context(), "team-a")
	if err != nil {
		t.Fatal(err)
	}
	request := func(runID string, fenced bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+runID+"/cache-grant", nil)
		req.SetPathValue("id", runID)
		if fenced {
			req.Header.Set(store.TriggerGenerationHeader, "1")
		}
		req = req.WithContext(contextWithPrincipal(req.Context(), &Principal{
			Name: issued.Claim.Principal, TokenPrefix: issued.Claim.TokenPrefix, Kind: store.TokenKindRunner, Team: "team-a",
		}))
		rec := httptest.NewRecorder()
		s.handleRunCacheGrant(teamA).ServeHTTP(rec, req)
		return rec
	}
	writeRef := func(trig store.Trigger) string {
		t.Helper()
		trig.Pipeline, trig.CreatedAt, trig.RepoURL = "demo", time.Now(), "https://github.com/acme/app.git"
		if err := team.CreateTrigger(t.Context(), trig); err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.DB().ExecContext(t.Context(), `UPDATE triggers SET status = 'claimed', claim_principal = ?, claim_token_prefix = ?, claim_seq = 1, lease_expires_at = ? WHERE id = ?`,
			issued.Claim.Principal, issued.Claim.TokenPrefix, time.Now().Add(time.Hour).UnixNano(), trig.ID); err != nil {
			t.Fatal(err)
		}
		grant, err := authwire.VerifyCacheGrant("grant-key", decodeGrant(t, request(trig.ID, true)).Grant, time.Now())
		if err != nil || grant.Scope == nil || len(grant.Scope.Refs) == 0 {
			t.Fatalf("grant for %s = %+v, %v", trig.ID, grant, err)
		}
		return grant.Scope.Refs[0]
	}
	const sha, other = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	push := map[string]string{sparkwing.EnvGitHubEventName: "push", "GITHUB_REF": "refs/heads/main"}
	bundle := map[string]string{bincache.SourceBundleObjectEnvKey: "sources/x"}
	schedule := map[string]string{crons.ScheduleEnvKey: "sched-1"}
	cases := []struct {
		trig store.Trigger
		want string
	}{
		{store.Trigger{ID: "push-1", TriggerSource: oidcWebhookSource, GitBranch: "main", GitSHA: sha, TriggerEnv: push}, "refs/heads/main"},
		{store.Trigger{ID: "retry-1", TriggerSource: "retry", RetryOf: "push-1", GitBranch: "main", GitSHA: sha}, "refs/heads/main"},
		{store.Trigger{ID: "child-1", TriggerSource: "api", ParentRunID: "retry-1", GitBranch: "main", GitSHA: sha}, "refs/heads/main"},
		{store.Trigger{ID: "retry-other", TriggerSource: "retry", RetryOf: "push-1", GitBranch: "main", GitSHA: other}, "manual:refs/heads/main"},
		{store.Trigger{ID: "child-bundle", ParentRunID: "push-1", GitBranch: "main", GitSHA: sha, TriggerEnv: bundle}, "manual:refs/heads/main"},
		{store.Trigger{ID: "cron-follow", TriggerSource: cronTriggerSource, GitBranch: "main", TriggerEnv: schedule}, "refs/heads/main"},
		{store.Trigger{ID: "cron-pinned", TriggerSource: cronTriggerSource, GitBranch: "main", GitSHA: other, TriggerEnv: schedule}, "manual:refs/heads/main"},
		{store.Trigger{ID: "cli-1", TriggerSource: "cli", GitBranch: "main", GitSHA: sha}, "manual:refs/heads/main"},
		{store.Trigger{ID: "retry-cli", TriggerSource: "retry", RetryOf: "cli-1", GitBranch: "main", GitSHA: sha}, "manual:refs/heads/main"},
	}
	for _, c := range cases {
		if got := writeRef(c.trig); got != c.want {
			t.Errorf("%s writes under %q, want %q", c.trig.ID, got, c.want)
		}
	}
	if rec := request("cli-1", false); rec.Code != http.StatusForbidden {
		t.Errorf("manual run's grant with no fence = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// Two servers on one host are two repositories, so their caches stay apart.
func TestCacheGrantKeepsTheRepositoryPort(t *testing.T) {
	s, _, _ := downloadFixture(t)
	team, err := s.store.ForTeam(t.Context(), "team-a")
	if err != nil {
		t.Fatal(err)
	}
	var repos []string
	for id, url := range map[string]string{"port-a": "https://git.example.com:8443/acme/app.git", "port-b": "https://git.example.com:9443/acme/app.git"} {
		if err := team.CreateTrigger(t.Context(), store.Trigger{ID: id, Pipeline: "demo", CreatedAt: time.Now(), RepoURL: url, GitBranch: "main"}); err != nil {
			t.Fatal(err)
		}
		scope, err := s.cacheGrantScope(t.Context(), "team-a", id)
		if err != nil {
			t.Fatal(err)
		}
		repos = append(repos, scope.Repo)
	}
	if repos[0] == repos[1] || repos[0] == "" {
		t.Fatalf("repositories on two ports share the cache scope %q", repos[0])
	}
}
