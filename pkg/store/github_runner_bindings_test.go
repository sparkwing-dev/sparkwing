package store_test

import (
	"context"

	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
