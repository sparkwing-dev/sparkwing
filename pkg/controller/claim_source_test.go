package controller_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (f *appFixture) launchedRun(who signedIn, runID, owner, name string) string {
	f.t.Helper()
	ctx := store.WithoutCreditMetering(context.Background())
	now := time.Now()
	if _, err := f.store.DB().ExecContext(ctx, `INSERT INTO repos (team, repo, dispatch, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT DO NOTHING`, who.team, store.RepoKey(owner, name), string(store.RepoDispatchController), now.UnixNano()); err != nil {
		f.t.Fatal(err)
	}
	tn, err := f.store.ForTeam(ctx, store.Team(who.team))
	if err != nil {
		f.t.Fatal(err)
	}
	if err := tn.CreateTriggerWithRun(ctx, store.Trigger{
		ID: runID, Pipeline: "build", GithubOwner: owner, GithubRepo: name, Repo: owner + "/" + name,
		GithubRepoID: map[string]int64{"acme/widgets": 701, "acme/plans": 702}[owner+"/"+name],
		GitBranch:    "main", GitSHA: headSHA, CreatedAt: now,
	}, store.Run{
		ID: runID, Pipeline: "build", Status: "pending", GithubOwner: owner, GithubRepo: name,
		DeclaredRepo: owner + "/" + name, CreatedAt: now, StartedAt: now,
	}); err != nil {
		f.t.Fatal(err)
	}
	return controller.LauncherFixtureClaim(f.t, f.store, runID, store.PlanNodeID)
}

// The token reads the repositories an owner approved by their GitHub IDs, so
// renaming an approved repository and giving its old name to another one
// changes nothing it reaches.
func TestRunSourceCredential_MintsTheApprovedRepositoriesByID(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code, out := f.setExtraRepos(olga, "acme/widgets", []string{"acme/plans"}); code != http.StatusOK {
		t.Fatalf("extra repos = %d %v", code, out)
	}
	f.app.SetRepos(7, githubapptest.Repo{ID: 701, FullName: "acme/widgets"},
		githubapptest.Repo{ID: 702, FullName: "acme/plans-archive", Private: true},
		githubapptest.Repo{ID: 703, FullName: "acme/plans", Private: true})
	tok := f.launchedRun(olga, "run-renamed", "acme", "widgets")
	var sc store.SourceCredential
	if code := f.call("POST", "/api/v1/runs/run-renamed/source-credential", "Bearer "+tok, nil, &sc); code != http.StatusOK {
		t.Fatalf("source credential = %d", code)
	}
	if !f.app.TokenCovers(sc.Token, "acme/plans-archive") || f.app.TokenCovers(sc.Token, "acme/plans") {
		t.Fatal("the token followed the name to another repository")
	}
}

// A repository no installation of the team covers gets no credential: Cloud
// fetches only through the GitHub App.
func TestRunSourceCredential_NeedsTheGitHubApp(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	tok := f.launchedRun(olga, "run-bare", "acme", "widgets")
	var out map[string]any
	if code := f.call("POST", "/api/v1/runs/run-bare/source-credential", "Bearer "+tok, nil, &out); code != http.StatusNotFound {
		t.Fatalf("uncovered = %d %v, want 404", code, out)
	}
}
