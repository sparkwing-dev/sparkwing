package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

var widgets = store.GitHubRepo{Owner: "Acme", Name: "Widgets"}

var mainPush = store.GitHubRunnerPush{Branch: "main", SHA: "0123456789abcdef0123456789abcdef01234567"}

func widgetsScope() store.GitHubRunnerScope {
	return store.GitHubRunnerScope{Team: "acme", Repo: widgets, Push: mainPush}
}

func githubWork(t *testing.T, s *store.Store, tenant *store.Tenant, runID, slug string, env map[string]string) {
	t.Helper()
	ctx := context.Background()
	repo, ok := store.ParseGitHubRepo(slug)
	if !ok {
		t.Fatalf("bad slug %q", slug)
	}
	now := time.Now()
	if env == nil {
		env = map[string]string{}
	}
	if _, ok := env["GITHUB_EVENT_NAME"]; !ok {
		env["GITHUB_EVENT_NAME"] = "push"
	}
	if err := tenant.CreateTriggerWithRun(ctx, store.Trigger{
		ID: runID, Pipeline: "build", Repo: slug, RepoURL: "https://github.com/" + slug + ".git",
		GithubOwner: repo.Owner, GithubRepo: repo.Name, TriggerSource: "github", TriggerEnv: env, CreatedAt: now,
		WebhookDelivery: runID, WebhookReplayKey: "signed-" + runID,
		GitBranch: mainPush.Branch, GitSHA: mainPush.SHA,
	}, store.Run{
		ID: runID, Pipeline: "build", Status: "pending", DeclaredRepo: slug,
		GithubOwner: repo.Owner, GithubRepo: repo.Name, CreatedAt: now, StartedAt: now,
		GitBranch: mainPush.Branch, GitSHA: mainPush.SHA,
	}); err != nil {
		t.Fatalf("create %s: %v", runID, err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: runID, NodeID: "compile", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := s.MarkNodeReady(ctx, runID, "compile"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
}

func TestGitHubRunnerScopeRefusesOtherTriggerProvenance(t *testing.T) {
	cases := []struct {
		name, source, event, retryOf, parent string
		allowed                              bool
	}{
		{"push", "github", "push", "", "", true},
		{"pull request", "github", "pull_request", "", "", false},
		{"missing event", "github", "", "", "", false},
		{"unsigned submission", "github", "push", "", "", false},
		{"retry", "github", "push", "prior", "", false},
		{"child", "github", "push", "", "prior", false},
		{"manual", "manual", "push", "", "", false},
		{"CLI", "cli", "push", "", "", false},
		{"cron", "schedule", "push", "", "", false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := storetest.Open(t)
			acme := teamHandle(t, st, "acme")
			id := githubClaimant(t, acme, "github:42:acme/widgets")
			runID := fmt.Sprintf("provenance-%d", i)
			env := map[string]string{"GITHUB_EVENT_NAME": tc.event, "GITHUB_REPOSITORY": "acme/widgets"}
			githubWork(t, st, acme, runID, "acme/widgets", env)
			if tc.name == "unsigned submission" {
				_, err := st.DB().Exec(storetest.Rebind(st, `UPDATE triggers SET webhook_delivery = '', webhook_replay_key = '' WHERE id = ?`), runID)
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := st.DB().Exec(storetest.Rebind(st, `UPDATE triggers SET trigger_source = ?, retry_of = ?, parent_run_id = ? WHERE id = ?`), tc.source, tc.retryOf, tc.parent, runID)
			if err != nil {
				t.Fatal(err)
			}
			ok, err := st.GitHubRunnerAdmits(context.Background(), widgetsScope(), runID)
			if err != nil || ok != tc.allowed {
				t.Fatalf("admitted = %v, %v; want %v", ok, err, tc.allowed)
			}
			ctx := store.WithGitHubRunnerScope(context.Background(), widgetsScope())
			if !tc.allowed {
				if n, err := st.ClaimNextReadyNode(ctx, id, "gh-1", time.Minute, nil); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("node claim = %+v, %v", n, err)
				}
			}
		})
	}
}

func TestGitHubRunnerScopeClaimsOnlyItsRepositorysNodes(t *testing.T) {
	st := storetest.Open(t)
	acme, other := teamHandle(t, st, "acme"), teamHandle(t, st, "other")
	githubWork(t, st, acme, "run-gadgets", "acme/gadgets", nil)
	githubWork(t, st, other, "run-other-widgets", "Acme/Widgets", nil)
	githubWork(t, st, acme, "run-forged-env", "Acme/Widgets", map[string]string{"GITHUB_REPOSITORY": "acme/gadgets"})
	githubWork(t, st, acme, "run-widgets", "Acme/Widgets", nil)

	ctx := store.WithGitHubRunnerScope(context.Background(), widgetsScope())
	id := githubClaimant(t, acme, "github:42:Acme/Widgets")
	n, err := st.ClaimNextReadyNode(ctx, id, "gh-1", time.Minute, nil)
	if err != nil || n.RunID != "run-widgets" {
		t.Fatalf("claim = %+v, %v; want run-widgets", n, err)
	}
	if n, err := st.ClaimNextReadyNode(ctx, id, "gh-2", time.Minute, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second claim = %+v, %v; want nothing: the rest is another repository's or team's", n, err)
	}
	if n, err := st.ClaimNextReadyNode(context.Background(), id, "local", time.Minute, nil); err != nil || n.RunID == "run-widgets" {
		t.Fatalf("unscoped claim = %+v, %v", n, err)
	}
}

func TestTriggerNamesGitHubRepoAcceptsEverySpellingOfTheRepository(t *testing.T) {
	for _, url := range []string{
		"https://github.com/acme/widgets", "https://github.com/Acme/Widgets.git", "git@github.com:acme/widgets.git",
		"ssh://git@github.com/acme/widgets", "",
	} {
		tr := &store.Trigger{GithubOwner: "acme", GithubRepo: "WIDGETS", RepoURL: url, Repo: "acme/widgets"}
		if !store.TriggerNamesGitHubRepo(tr, widgets) {
			t.Errorf("repo_url %q refused", url)
		}
	}
	for name, tr := range map[string]*store.Trigger{
		"other owner":      {GithubOwner: "evil", GithubRepo: "widgets"},
		"no github fields": {RepoURL: "https://github.com/acme/widgets"},
		"other url":        {GithubOwner: "acme", GithubRepo: "widgets", RepoURL: "https://github.com/acme/gadgets"},
		"other host":       {GithubOwner: "acme", GithubRepo: "widgets", RepoURL: "https://gitlab.com/acme/widgets"},
		"suffix":           {GithubOwner: "acme", GithubRepo: "widgets", Repo: "acme/widgets-fork"},
		"other env":        {GithubOwner: "acme", GithubRepo: "widgets", TriggerEnv: map[string]string{"GITHUB_REPOSITORY": "acme/gadgets"}},
	} {
		if store.TriggerNamesGitHubRepo(tr, widgets) {
			t.Errorf("%s accepted", name)
		}
	}
}

func githubClaimant(t *testing.T, team *store.Tenant, principal string) store.ClaimIdentity {
	t.Helper()
	_, tok, err := team.CreateToken(context.Background(), principal, store.TokenKindRunner, []string{"nodes.claim"}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return store.ClaimIdentity{Principal: principal, TokenPrefix: tok.Prefix}
}

// A credential is bound to the push its job ran for: the same repository's
// work on another branch, or at another commit of the same branch, is not its
// to claim.
func TestGitHubRunnerScopeClaimsOnlyItsPush(t *testing.T) {
	st := storetest.Open(t)
	acme := teamHandle(t, st, "acme")
	githubWork(t, st, acme, "run-main", "Acme/Widgets", nil)
	id := githubClaimant(t, acme, "github:42:Acme/Widgets")
	for name, push := range map[string]store.GitHubRunnerPush{
		"another branch":           {Branch: "feature", SHA: mainPush.SHA},
		"another commit on main":   {Branch: "main", SHA: "fedcba9876543210fedcba9876543210fedcba98"},
		"a scope naming no commit": {Branch: "main"},
	} {
		scope := widgetsScope()
		scope.Push = push
		ctx := store.WithGitHubRunnerScope(context.Background(), scope)
		if n, err := st.ClaimNextReadyNode(ctx, id, "gh-1", time.Minute, nil); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: node claim = %+v, %v; want nothing", name, n, err)
		}
		if ok, err := st.GitHubRunnerAdmits(context.Background(), scope, "run-main"); err != nil || ok {
			t.Errorf("%s: admits run-main = %v, %v; want false", name, ok, err)
		}
	}
	if ok, err := st.GitHubRunnerAdmits(context.Background(), widgetsScope(), "run-main"); err != nil || !ok {
		t.Fatalf("its own push: admits = %v, %v; want true", ok, err)
	}
}
