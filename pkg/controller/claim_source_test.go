package controller_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
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
	c, err := f.store.ClaimLaunch(ctx, store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"},
		store.LaunchClaimRequest{
			HolderID: "launcher:" + runID, Lease: time.Minute, Deadline: time.Hour,
			RunID: runID, NodeID: store.PlanNodeID,
		}, time.Now())
	if err != nil || c == nil {
		f.t.Fatalf("launch claim %s: %+v %v", runID, c, err)
	}
	return c.Token
}

// The init container's one ask gets an App token that reads exactly the run's
// repository and the ones its owner listed, with the run's commit; a second
// ask with the same claim token, which the pipeline's container also holds,
// is refused and mints nothing, as is an ask for another run.
func TestRunSourceCredential_IssuesOneScopedTokenPerClaim(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code, out := f.setExtraRepos(olga, "acme/widgets", []string{"acme/plans"}); code != http.StatusOK {
		t.Fatalf("extra repos = %d %v", code, out)
	}
	tok := f.launchedRun(olga, "run-src", "acme", "widgets")
	before := len(f.app.Minted())
	var sc store.SourceCredential
	if code := f.call("POST", "/api/v1/runs/run-src/source-credential", "Bearer "+tok, nil, &sc); code != http.StatusOK {
		t.Fatalf("source credential = %d", code)
	}
	if sc.RepoURL != "https://github.com/acme/widgets.git" || sc.SHA != headSHA || sc.Branch != "main" ||
		!slices.Equal(sc.Repositories, []string{"acme/widgets", "acme/plans"}) || sc.Source != (store.SourceSpec{Depth: 1}) {
		t.Fatalf("source credential = %+v", sc)
	}
	minted := f.app.Minted()[before:]
	if len(minted) != 1 || len(minted[0].Repositories) != 0 || !slices.Equal(minted[0].RepositoryIDs, []int64{701, 702}) ||
		len(minted[0].Permissions) != 1 || minted[0].Permissions["contents"] != "read" {
		t.Fatalf("minted = %+v, want one read-only token for widgets and plans by id", minted)
	}
	if !f.app.TokenCovers(sc.Token, "acme/plans") || f.app.TokenCovers(sc.Token, "bob/tools") {
		t.Fatal("the token's reach differs from the listed repositories")
	}
	// safety: a mint that failed or whose answer was lost is asked for again, up to the limit.
	for i := 1; i < store.MaxSourceMints; i++ {
		if code := f.call("POST", "/api/v1/runs/run-src/source-credential", "Bearer "+tok, nil, &sc); code != http.StatusOK {
			t.Fatalf("ask %d = %d, want a retry within the limit", i+1, code)
		}
	}
	var refused map[string]any
	if code := f.call("POST", "/api/v1/runs/run-src/source-credential", "Bearer "+tok, nil, &refused); code != http.StatusForbidden ||
		refused["error"] != "source_credential_spent" {
		t.Fatalf("ask past the limit = %d %v, want 403 source_credential_spent", code, refused)
	}
	f.launchedRun(olga, "run-sibling", "acme", "widgets")
	if code := f.call("POST", "/api/v1/runs/run-sibling/source-credential", "Bearer "+tok, nil, &refused); code != http.StatusForbidden {
		t.Fatalf("a sibling run's credential = %d, want 403", code)
	}
	if n := len(f.app.Minted()) - before; n != store.MaxSourceMints {
		t.Fatalf("minted %d tokens, want %d", n, store.MaxSourceMints)
	}
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

// A claim token gets a cache grant bound to its claim: every controller use
// of the grant re-checks the claim, so it stops working when the run is
// cancelled, and a plan claim's grant uploads only a binary.
func TestRunCacheGrant_IsBoundToTheClaim(t *testing.T) {
	f := newAppFixture(t)
	f.srv.WithCacheGrantKey("claim-grant-signing-key")
	olga := f.ghUser(501, "olga")
	tok := f.launchedRun(olga, "run-grant", "acme", "widgets")
	var out controller.CacheGrantResponse
	if code := f.call("POST", "/api/v1/runs/run-grant/cache-grant", "Bearer "+tok, map[string]any{}, &out); code != http.StatusOK {
		t.Fatalf("cache grant = %d", code)
	}
	claim, err := f.store.AuthorizeClaimToken(context.Background(), tok, store.ClaimResult, out.ExpiresAt)
	if err != nil || claim.ExpiresAt.Sub(out.ExpiresAt) < time.Hour-controller.ClaimCacheGrantTTL-time.Minute {
		t.Fatalf("the grant expires %s, the claim token %s (%v); want the grant within %s of its mint",
			out.ExpiresAt, claim.ExpiresAt, err, controller.ClaimCacheGrantTTL)
	}
	grant, err := authwire.VerifyCacheGrant("claim-grant-signing-key", out.Grant, time.Now())
	if err != nil || grant.Claim == nil || grant.Claim.Kind != authwire.CacheClaimToken || grant.Team != olga.team ||
		grant.Run != "run-grant" || grant.Claim.NodeID != store.PlanNodeID {
		t.Fatalf("grant = %+v, %v", grant, err)
	}
	ctx := context.Background()
	if binaryOnly, err := controller.VerifyLiveDataGrant(ctx, f.srv, out.Grant); err != nil || !binaryOnly {
		t.Fatalf("live plan grant = %v, %v; want accepted and binary-only", binaryOnly, err)
	}
	if err := f.teamOf(olga).RequestCancel(ctx, "run-grant"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.VerifyLiveDataGrant(ctx, f.srv, out.Grant); err == nil {
		t.Fatal("the grant outlived the claim's cancel")
	}
	if code := f.call("POST", "/api/v1/runs/run-grant/cache-grant", "Bearer "+tok, map[string]any{}, &out); code != http.StatusForbidden {
		t.Fatalf("cache grant after cancel = %d, want 403", code)
	}
}
