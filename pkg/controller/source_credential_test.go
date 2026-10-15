package controller_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type sourceAnswer struct {
	store.SourceCredential
	Error   string `json:"error"`
	Message string `json:"message"`
}

func (f *appFixture) sourceCredential(auth, runID string) (sourceAnswer, int) {
	f.t.Helper()
	var out sourceAnswer
	code := f.call("POST", "/api/v1/runs/"+runID+"/source-credential", auth, nil, &out)
	return out, code
}

func (f *appFixture) createRun(owner signedIn, runID, repoURL string, repoID int64) {
	f.t.Helper()
	ctx := context.Background()
	tn, err := f.store.ForTeam(ctx, store.Team(owner.team))
	if err != nil {
		f.t.Fatal(err)
	}
	now := time.Now()
	if err := tn.CreateTriggerWithRun(ctx, store.Trigger{
		ID: runID, Pipeline: "build", RepoURL: repoURL, GithubRepoID: repoID,
		CreatedAt: now, GitBranch: "main", GitSHA: headSHA,
	}, store.Run{
		ID: runID, Pipeline: "build", Status: "pending", CreatedAt: now, StartedAt: now, RepoURL: repoURL,
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *appFixture) runWork(owner signedIn, runID, repoURL string) (auth, prefix string) {
	bearer, _, prefix := f.claimRunNode(owner, runID, repoURL, 0)
	return bearer, prefix
}

func (f *appFixture) nodeClaim(owner signedIn, runID, repoURL string, repoID int64) (auth, prefix string) {
	_, claim, prefix := f.claimRunNode(owner, runID, repoURL, repoID)
	return claim, prefix
}

func (f *appFixture) claimRunNode(owner signedIn, runID, repoURL string, repoID int64) (bearer, claim, prefix string) {
	f.t.Helper()
	ctx := context.Background()
	f.createRun(owner, runID, repoURL, repoID)
	if err := f.store.CreateNode(ctx, store.Node{RunID: runID, NodeID: "compile", Status: "pending"}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.MarkNodeReady(ctx, runID, "compile"); err != nil {
		f.t.Fatal(err)
	}
	bearer, prefix = f.teamRunner(owner, "r-"+runID)
	var n claimedNode
	if code := f.call("POST", "/api/v1/runs/"+runID+"/nodes/compile/claim", bearer,
		map[string]any{"holder_id": "pod-" + runID}, &n); code != http.StatusOK || n.RunID != runID ||
		!strings.HasPrefix(n.ClaimToken, store.ClaimTokenPrefix+"_") {
		f.t.Fatalf("claim %s = %d %+v", runID, code, n)
	}
	return bearer, "Bearer " + n.ClaimToken, prefix
}

func (f *appFixture) teamRunner(owner signedIn, name string) (auth, prefix string) {
	f.t.Helper()
	var m mintedRunner
	if code := f.call("POST", "/api/v1/team/tokens", owner.auth,
		map[string]any{"scopes": machineScopes, "name": name, "repos": []string{"github.com/*/*", "gitlab.example.com/*/*", "git.example.invalid/*/*"}}, &m); code != http.StatusCreated {
		f.t.Fatalf("mint runner = %d", code)
	}
	return "Bearer " + m.Token, m.Prefix
}

func (f *appFixture) setExtraRepos(who signedIn, repo string, extras []string) (int, map[string]any) {
	f.t.Helper()
	var out map[string]any
	code := f.call("PUT", "/api/v1/team/github-app/extra-repos", who.auth,
		map[string]any{"repository": repo, "extra_repos": extras}, &out)
	return code, out
}

// A run the App started gets a token that reads its repository, named by the
// GitHub ID the webhook recorded, and nothing else of the installation.
func TestSourceCredential_AppTokenReadsTheVerifiedRepository(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	node, _ := f.nodeClaim(olga, "run-widgets", "git@github.com:acme/widgets.git", 701)
	before := len(f.app.Minted())
	got, code := f.sourceCredential(node, "run-widgets")
	if code != http.StatusOK || got.Kind != "github_app" || got.Host != "github.com" ||
		!slices.Equal(got.Repositories, []string{"acme/widgets"}) || got.SHA != headSHA {
		t.Fatalf("source credential = %d %+v, want the App token for acme/widgets", code, got)
	}
	minted := f.app.Minted()[before:]
	if len(minted) != 1 || !slices.Equal(minted[0].RepositoryIDs, []int64{701}) || len(minted[0].Repositories) != 0 ||
		len(minted[0].Permissions) != 1 || minted[0].Permissions["contents"] != "read" {
		t.Fatalf("minted = %+v, want one read-only token for 701 by id", minted)
	}
	if !f.app.TokenCovers(got.Token, "acme/widgets") || f.app.TokenCovers(got.Token, "acme/plans") {
		t.Fatal("the App token does not read exactly the run's repository")
	}
}

// A repository a run names by hand gets an App token only once the team
// subscribed it, so an editor who types a private repository of the
// installation into a run reaches nothing it could not already trigger.
func TestSourceCredential_TypedRepositoryNeedsASubscription(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	node, _ := f.nodeClaim(olga, "run-plans", "https://github.com/acme/plans.git", 0)
	before := len(f.app.Minted())
	got, code := f.sourceCredential(node, "run-plans")
	if code != http.StatusNotFound || got.Error != "no_source_credential" || got.Token != "" ||
		!strings.Contains(got.Message, "subscribes it") {
		t.Fatalf("an unsubscribed private repository = %d %+v, want no_source_credential naming the subscription", code, got)
	}
	if n := len(f.app.Minted()) - before; n != 0 {
		t.Fatalf("the refusal minted %d tokens", n)
	}
	if code := f.subscribe(olga, "acme/plans", "build", nil); code != http.StatusOK {
		t.Fatalf("subscribe = %d", code)
	}
	got, code = f.sourceCredential(node, "run-plans")
	if code != http.StatusOK || got.Kind != "github_app" || !f.app.TokenCovers(got.Token, "acme/plans") ||
		f.app.TokenCovers(got.Token, "acme/widgets") {
		t.Fatalf("a subscribed repository = %d %+v, want the App token for acme/plans alone", code, got)
	}
	minted := f.app.Minted()
	if last := minted[len(minted)-1]; !slices.Equal(last.RepositoryIDs, []int64{702}) || last.Permissions["contents"] != "read" {
		t.Fatalf("minted = %+v, want the subscription's repository id", last)
	}
}

// With nothing that covers the run, the route answers the code a runner reads
// as "no credential", and a message naming what the team can do about it.
func TestSourceCredential_NamesTheRemedy(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	cases := map[string]struct {
		repo string
		id   int64
		want string
	}{
		"uncovered github": {"https://github.com/someone/else.git", 999, "install the team's GitHub App on someone/else"},
		"typed github":     {"https://github.com/acme/widgets.git", 0, "subscribes it"},
		"another host":     {"https://gitlab.example.com/acme/tools.git", 0, "store a git credential for gitlab.example.com"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			runID := "run-" + strings.ReplaceAll(name, " ", "-")
			node, _ := f.nodeClaim(olga, runID, tc.repo, tc.id)
			got, code := f.sourceCredential(node, runID)
			if code != http.StatusNotFound || got.Error != "no_source_credential" || !strings.Contains(got.Message, tc.want) {
				t.Fatalf("source credential = %d %+v, want no_source_credential naming %q", code, got, tc.want)
			}
			if !strings.Contains(got.Message, "Git credentials") {
				t.Fatalf("the remedy %q does not name the team's git credentials", got.Message)
			}
		})
	}
}

// Only the run's own claim token is answered: the machine's bearer, even
// while it holds the claim, gets the claim-required refusal, and another
// team's claim token is refused.
func TestSourceCredential_NeedsTheRunsOwnClaimToken(t *testing.T) {
	f := newAppFixture(t)
	olga, bob := f.ghUser(501, "olga"), f.ghUser(502, "bob")
	f.connect(olga, 501, 7, acmeAdmin)
	f.createRun(olga, "run-widgets", "https://github.com/acme/widgets.git", 701)
	if err := f.store.CreateNode(context.Background(), store.Node{RunID: "run-widgets", NodeID: "compile", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkNodeReady(context.Background(), "run-widgets", "compile"); err != nil {
		t.Fatal(err)
	}
	machine, _ := f.teamRunner(olga, "holder")
	if code := f.call("POST", "/api/v1/runs/run-widgets/nodes/compile/claim", machine,
		map[string]any{"holder_id": "pod-widgets"}, nil); code != http.StatusOK {
		t.Fatalf("claim = %d", code)
	}
	idle, _ := f.teamRunner(olga, "idle")
	for name, auth := range map[string]string{"the claim's machine": machine, "an idle machine": idle, "the owner": olga.auth} {
		if got, code := f.sourceCredential(auth, "run-widgets"); code != http.StatusForbidden || got.Error != "claim_token_required" || got.Token != "" {
			t.Fatalf("%s = %d %+v, want 403 claim_token_required", name, code, got)
		}
	}
	bobs, _ := f.nodeClaim(bob, "run-bob", "https://github.com/bob/tools.git", 801)
	if got, code := f.sourceCredential(bobs, "run-widgets"); (code != http.StatusForbidden && code != http.StatusNotFound) ||
		got.Token != "" || got.Error == "no_source_credential" {
		t.Fatalf("another team's claim token = %d %+v, want a refusal that reveals nothing", code, got)
	}
}

// A runner's trigger claim fetches the run's source with its own claim token.
func TestSourceCredential_TriggerClaimFetchesTheRunsSource(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.createRun(olga, "run-hook", "https://github.com/acme/widgets.git", 701)
	machine, _ := f.teamRunner(olga, "trigger-runner")
	trig, err := client.NewWithToken(f.url, nil, strings.TrimPrefix(machine, "Bearer ")).ClaimSpecificTrigger(context.Background(), "run-hook", time.Minute)
	if err != nil || trig == nil || !strings.HasPrefix(trig.ClaimToken, store.ClaimTokenPrefix+"_") {
		t.Fatalf("claim trigger = %+v, %v", trig, err)
	}
	got, code := f.sourceCredential("Bearer "+trig.ClaimToken, "run-hook")
	if code != http.StatusOK || got.Kind != "github_app" || !f.app.TokenCovers(got.Token, "acme/widgets") {
		t.Fatalf("trigger claim = %d %+v, want the App token", code, got)
	}
}

func TestSourceCredential_AppTokenReadsOnlyTheOwnersExtraRepos(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	node, _ := f.nodeClaim(olga, "run-widgets", "https://github.com/acme/widgets.git", 701)

	var asked sourceAnswer
	code := f.call("POST", "/api/v1/runs/run-widgets/source-credential", node, map[string]any{"extra_repos": []string{"acme/plans"}}, &asked)
	if code != http.StatusOK || f.app.TokenCovers(asked.Token, "acme/plans") || len(asked.Repositories) != 1 {
		t.Fatalf("a runner naming acme/plans itself = %d %+v; its token must read the run's repository alone", code, asked)
	}

	eddie := f.user("e", "eddie@example.com")
	f.join(olga, eddie, "eddie@example.com", "editor")
	eddie.team = olga.team
	if code, _ := f.setExtraRepos(eddie, "acme/widgets", []string{"acme/plans"}); code != http.StatusForbidden {
		t.Fatalf("an editor sets the list = %d, want 403", code)
	}
	if code, _ := f.setExtraRepos(olga, "acme/widgets", []string{"acme/secret"}); code != http.StatusBadRequest {
		t.Fatalf("a repository the installation does not cover = %d, want 400", code)
	}
	if code, _ := f.setExtraRepos(olga, "acme/widgets", []string{"bob/tools"}); code != http.StatusBadRequest {
		t.Fatalf("a repository of another owner = %d, want 400", code)
	}
	tooMany := make([]string, 11)
	for i := range tooMany {
		tooMany[i] = "acme/r" + string(rune('a'+i))
	}
	if code, _ := f.setExtraRepos(olga, "acme/widgets", tooMany); code != http.StatusBadRequest {
		t.Fatalf("eleven extras = %d, want 400", code)
	}
	if code, out := f.setExtraRepos(olga, "acme/widgets", []string{"acme/plans"}); code != http.StatusOK {
		t.Fatalf("the owner sets the list = %d %v", code, out)
	}
	var list struct {
		ExtraRepos []struct {
			Repository string   `json:"repository"`
			ExtraRepos []string `json:"extra_repos"`
		} `json:"extra_repos"`
	}
	if code := f.call("GET", "/api/v1/team/github-app/extra-repos", eddie.auth, nil, &list); code != http.StatusOK ||
		len(list.ExtraRepos) != 1 || list.ExtraRepos[0].Repository != "acme/widgets" || len(list.ExtraRepos[0].ExtraRepos) != 1 {
		t.Fatalf("a member reads the list = %d %+v", code, list)
	}

	got, code := f.sourceCredential(node, "run-widgets")
	if code != http.StatusOK || got.Kind != "github_app" || !slices.Equal(got.Repositories, []string{"acme/widgets", "acme/plans"}) {
		t.Fatalf("source credential once listed = %d %+v", code, got)
	}
	if !f.app.TokenCovers(got.Token, "acme/widgets") || !f.app.TokenCovers(got.Token, "acme/plans") {
		t.Fatal("the token does not read both the run's repository and the listed extra")
	}

	f.app.SetRepos(7, githubapptest.Repo{ID: 701, FullName: "acme/widgets"})
	_, url := f.replica()
	f.url = url
	if got, code := f.sourceCredential(node, "run-widgets"); code != http.StatusConflict || !strings.Contains(got.Error, "acme/plans") {
		t.Fatalf("a listed repository the installation stopped covering = %d %+v, want 409 naming it", code, got)
	}
}

// The source-token and git-credential routes are gone: a runner's bearer that
// asks either gets the mux's 404, a node's claim token is refused before
// routing, and nothing is minted.
func TestSourceCredential_RemovedRoutesAnswer404(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	node, _ := f.nodeClaim(olga, "run-widgets", "https://github.com/acme/widgets.git", 701)
	machine, _ := f.teamRunner(olga, "asker")
	before := len(f.app.Minted())
	for _, route := range []string{"source-token", "git-credential"} {
		if code := f.call("POST", "/api/v1/runs/run-widgets/"+route, machine, nil, nil); code != http.StatusNotFound {
			t.Fatalf("%s with the machine's bearer = %d, want 404", route, code)
		}
		if code := f.call("POST", "/api/v1/runs/run-widgets/"+route, node, nil, nil); code != http.StatusForbidden {
			t.Fatalf("%s with the node's claim token = %d, want 403", route, code)
		}
	}
	if n := len(f.app.Minted()) - before; n != 0 {
		t.Fatalf("the removed routes minted %d tokens", n)
	}
}
