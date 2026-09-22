package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

var widgets = store.GitHubRepo{Owner: "Acme", Name: "Widgets"}

// githubWork writes a trigger, its run and one ready node for repo into
// tenant's team, spelled the way a push webhook records it.
func githubWork(t *testing.T, s *store.Store, tenant *store.Tenant, runID, slug string, env map[string]string) {
	t.Helper()
	ctx := context.Background()
	repo, ok := store.ParseGitHubRepo(slug)
	if !ok {
		t.Fatalf("bad slug %q", slug)
	}
	now := time.Now()
	if err := tenant.CreateTriggerWithRun(ctx, store.Trigger{
		ID: runID, Pipeline: "build", Repo: slug, RepoURL: "https://github.com/" + slug + ".git",
		GithubOwner: repo.Owner, GithubRepo: repo.Name, TriggerEnv: env, CreatedAt: now,
	}, store.Run{
		ID: runID, Pipeline: "build", Status: "pending", DeclaredRepo: slug,
		GithubOwner: repo.Owner, GithubRepo: repo.Name, CreatedAt: now, StartedAt: now,
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

func TestGitHubRunnerBindingsAreTeamScoped(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme, other := teamHandle(t, st, "acme"), teamHandle(t, st, "other")
	now := time.Now()

	b, err := acme.AddGitHubRunnerBinding(ctx, store.GitHubRunnerBinding{
		RepositoryID: 42, RepositoryOwnerID: 7, Repository: "Acme/Widgets", CreatedBy: "u1",
	}, now)
	if err != nil || b.Team != "acme" || b.Repository != "Acme/Widgets" {
		t.Fatalf("add = %+v, %v", b, err)
	}
	if _, err := acme.AddGitHubRunnerBinding(ctx, b, now); !errors.Is(err, store.ErrAlreadyBound) {
		t.Fatalf("second add = %v, want ErrAlreadyBound", err)
	}
	for _, bad := range []store.GitHubRunnerBinding{
		{RepositoryID: 1, RepositoryOwnerID: 1, Repository: "no-slash"},
		{RepositoryID: 0, RepositoryOwnerID: 1, Repository: "a/b"},
		{RepositoryID: 1, RepositoryOwnerID: 0, Repository: "a/b"},
		{RepositoryID: 1, RepositoryOwnerID: 1, Repository: "a/b c"},
	} {
		if _, err := acme.AddGitHubRunnerBinding(ctx, bad, now); !errors.Is(err, store.ErrInvalidInput) {
			t.Fatalf("add %+v = %v, want ErrInvalidInput", bad, err)
		}
	}
	if _, err := other.GitHubRunnerBinding(ctx, 42); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other team read acme's binding: %v", err)
	}
	if list, err := other.GitHubRunnerBindings(ctx); err != nil || len(list) != 0 {
		t.Fatalf("other team lists %+v, %v", list, err)
	}
	if _, err := other.RemoveGitHubRunnerBinding(ctx, 42, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other team removed acme's binding: %v", err)
	}
	if list, err := acme.GitHubRunnerBindings(ctx); err != nil || len(list) != 1 || list[0].RepositoryOwnerID != 7 {
		t.Fatalf("acme lists %+v, %v", list, err)
	}
}

func TestRemovingABindingRevokesItsCredentials(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, st, "acme")
	now := time.Now()
	if _, err := acme.AddGitHubRunnerBinding(ctx, store.GitHubRunnerBinding{
		RepositoryID: 42, RepositoryOwnerID: 7, Repository: "acme/widgets",
	}, now); err != nil {
		t.Fatal(err)
	}
	mint := func(principal string) *store.Token {
		_, tok, err := acme.CreateToken(ctx, principal, store.TokenKindRunner, []string{"nodes.claim"}, time.Hour, now)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	bound, unrelated, laptop := mint("github:42:acme/widgets"), mint("github:421:acme/gadgets"), mint("agent:laptop")
	if n, err := acme.LiveGitHubRunnerCredentials(ctx, now); err != nil || n != 2 {
		t.Fatalf("live credentials = %d, %v, want 2", n, err)
	}
	revoked, err := acme.RemoveGitHubRunnerBinding(ctx, 42, now)
	if err != nil || len(revoked) != 1 || revoked[0] != bound.Prefix {
		t.Fatalf("remove revoked %v, %v; want only %s", revoked, err, bound.Prefix)
	}
	later := now.Add(time.Second)
	if n, err := acme.LiveGitHubRunnerCredentials(ctx, later); err != nil || n != 1 {
		t.Fatalf("live credentials after remove = %d, %v, want 1", n, err)
	}
	for _, tok := range []*store.Token{unrelated, laptop} {
		if got, err := acme.RunnerToken(ctx, tok.Prefix); err != nil || got.RevokedAt != nil {
			t.Fatalf("%s = %+v, %v; want untouched", tok.Principal, got, err)
		}
	}
}

func TestGitHubRunnerScopeClaimsOnlyItsRepositorysNodes(t *testing.T) {
	st := storetest.Open(t)
	acme, other := teamHandle(t, st, "acme"), teamHandle(t, st, "other")
	githubWork(t, st, acme, "run-gadgets", "acme/gadgets", nil)
	githubWork(t, st, other, "run-other-widgets", "Acme/Widgets", nil)
	githubWork(t, st, acme, "run-forged-env", "Acme/Widgets", map[string]string{"GITHUB_REPOSITORY": "acme/gadgets"})
	githubWork(t, st, acme, "run-widgets", "Acme/Widgets", nil)

	ctx := store.WithGitHubRunnerScope(context.Background(), store.GitHubRunnerScope{Team: "acme", Repo: widgets})
	id := store.ClaimIdentity{Principal: "github:42:Acme/Widgets", TokenPrefix: "swr_aaaaaaaa"}
	n, err := st.ClaimNextReadyNode(ctx, id, "gh-1", time.Minute, nil)
	if err != nil || n.RunID != "run-widgets" {
		t.Fatalf("claim = %+v, %v; want run-widgets", n, err)
	}
	if n, err := st.ClaimNextReadyNode(ctx, id, "gh-2", time.Minute, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second claim = %+v, %v; want nothing: the rest is another repository's or team's", n, err)
	}
	// Control: the same queue without the scope hands out the other work.
	if n, err := st.ClaimNextReadyNode(context.Background(), id, "local", time.Minute, nil); err != nil || n.RunID == "run-widgets" {
		t.Fatalf("unscoped claim = %+v, %v", n, err)
	}
}

func TestGitHubRunnerScopeClaimsOnlyItsRepositorysTriggers(t *testing.T) {
	st := storetest.Open(t)
	acme, other := teamHandle(t, st, "acme"), teamHandle(t, st, "other")
	githubWork(t, st, acme, "run-gadgets", "acme/gadgets", nil)
	githubWork(t, st, other, "run-other-widgets", "acme/widgets", nil)
	ctx := store.WithGitHubRunnerScope(context.Background(), store.GitHubRunnerScope{Team: "acme", Repo: widgets})
	id := store.ClaimIdentity{Principal: "github:42:Acme/Widgets", TokenPrefix: "swr_aaaaaaaa"}
	if tr, err := st.ClaimNextTriggerFor(ctx, id, 0, nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim = %+v, %v; want nothing", tr, err)
	}
	githubWork(t, st, acme, "run-widgets", "acme/widgets", nil)
	tr, err := st.ClaimNextTriggerFor(ctx, id, 0, nil, nil)
	if err != nil || tr.ID != "run-widgets" {
		t.Fatalf("claim = %+v, %v; want run-widgets", tr, err)
	}
}

func TestGitHubRunnerScopeLeavesATriggerNamingAnotherRepositoryInItsEnv(t *testing.T) {
	st := storetest.Open(t)
	acme := teamHandle(t, st, "acme")
	githubWork(t, st, acme, "run-forged-env", "acme/widgets", map[string]string{"GITHUB_REPOSITORY": "acme/gadgets"})
	ctx := store.WithGitHubRunnerScope(context.Background(), store.GitHubRunnerScope{Team: "acme", Repo: widgets})
	id := store.ClaimIdentity{Principal: "github:42:acme/widgets", TokenPrefix: "swr_aaaaaaaa"}
	if tr, err := st.ClaimNextTriggerFor(ctx, id, 0, nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim = %+v, %v; want nothing", tr, err)
	}
	got, err := st.GetTrigger(context.Background(), "run-forged-env")
	if err != nil || got.Status != "pending" {
		t.Fatalf("trigger = %+v, %v; want it left pending", got, err)
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
