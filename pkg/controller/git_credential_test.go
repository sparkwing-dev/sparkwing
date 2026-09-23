package controller_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type gitCredentialAnswer struct {
	controller.GitCredentialResponse
	Username   string `json:"username"`
	Secret     string `json:"secret"`
	KnownHosts string `json:"known_hosts"`
	Error      string `json:"error"`
	Message    string `json:"message"`
}

func (f *appFixture) gitCredential(auth, runID string) (gitCredentialAnswer, int) {
	f.t.Helper()
	var out gitCredentialAnswer
	code := f.call("POST", "/api/v1/runs/"+runID+"/git-credential", auth, map[string]any{}, &out)
	return out, code
}

// runWork creates a run of owner's team from repoURL with one ready node,
// and returns a fresh runner token of the team holding the node's claim.
func (f *appFixture) runWork(owner signedIn, runID, repoURL string) (auth, prefix string) {
	f.t.Helper()
	ctx := context.Background()
	tn, err := f.store.ForTeam(ctx, store.Team(owner.team))
	if err != nil {
		f.t.Fatal(err)
	}
	now := time.Now()
	if err := tn.CreateTriggerWithRun(ctx, store.Trigger{
		ID: runID, Pipeline: "build", RepoURL: repoURL, CreatedAt: now, GitBranch: "main", GitSHA: headSHA,
	}, store.Run{
		ID: runID, Pipeline: "build", Status: "pending", CreatedAt: now, StartedAt: now, RepoURL: repoURL,
	}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.CreateNode(ctx, store.Node{RunID: runID, NodeID: "compile", Status: "pending"}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.MarkNodeReady(ctx, runID, "compile"); err != nil {
		f.t.Fatal(err)
	}
	auth, prefix = f.teamRunner(owner, "r-"+runID)
	var n claimedNode
	if code := f.call("POST", "/api/v1/runs/"+runID+"/nodes/compile/claim", auth,
		map[string]any{"holder_id": "pod-" + runID}, &n); code != http.StatusOK || n.RunID != runID {
		f.t.Fatalf("claim %s = %d %+v", runID, code, n)
	}
	return auth, prefix
}

func (f *appFixture) teamRunner(owner signedIn, name string) (auth, prefix string) {
	f.t.Helper()
	var m mintedRunner
	if code := f.call("POST", "/api/v1/team/runner-tokens", owner.auth,
		map[string]any{"name": name, "repos": []string{"github.com/*/*", "gitlab.example.com/*/*", "git.example.invalid/*/*"}}, &m); code != http.StatusCreated {
		f.t.Fatalf("mint runner = %d", code)
	}
	return "Bearer " + m.Token, m.Prefix
}

func TestGitCredentialReleasesTheAppTokenForACoveredRepo(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	runner, _ := f.runWork(olga, "run-widgets", "git@github.com:acme/widgets.git")
	got, code := f.gitCredential(runner, "run-widgets")
	if code != http.StatusOK || got.Kind != "github_app" || got.Host != "github.com" || got.Repository != "acme/widgets" {
		t.Fatalf("git credential = %d %+v, want the App token for acme/widgets", code, got)
	}
	if !f.app.TokenCovers(got.Token, "acme/widgets") || f.app.TokenCovers(got.Token, "acme/plans") {
		t.Fatal("the App token does not read exactly the run's repository")
	}
}

// With nothing that covers the run, the route answers the code a runner reads
// as "no credential", and a message naming what the team can do about it.
func TestGitCredentialNamesTheRemedy(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	cases := map[string]struct{ repo, want string }{
		"uncovered github": {"https://github.com/someone/else.git", "install the team's GitHub App on someone/else"},
		"another host":     {"https://gitlab.example.com/acme/tools.git", "store a git credential for gitlab.example.com"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			runID := "run-" + strings.ReplaceAll(name, " ", "-")
			runner, _ := f.runWork(olga, runID, tc.repo)
			got, code := f.gitCredential(runner, runID)
			if code != http.StatusNotFound || got.Error != "no_source_credential" || !strings.Contains(got.Message, tc.want) {
				t.Fatalf("git credential = %d %+v, want no_source_credential naming %q", code, got, tc.want)
			}
			if !strings.Contains(got.Message, "Git credentials") {
				t.Fatalf("the remedy %q does not name the team's git credentials", got.Message)
			}
		})
	}
}

func TestGitCredentialNeedsALiveClaimOnTheRun(t *testing.T) {
	f := newAppFixture(t)
	olga, bob := f.ghUser(501, "olga"), f.ghUser(502, "bob")
	f.connect(olga, 501, 7, acmeAdmin)
	f.runWork(olga, "run-widgets", "https://github.com/acme/widgets.git")
	idle, _ := f.teamRunner(olga, "idle")
	if _, code := f.gitCredential(idle, "run-widgets"); code != http.StatusForbidden {
		t.Fatalf("a runner of the team with no claim = %d, want 403", code)
	}
	bobs, _ := f.runWork(bob, "run-bob", "https://github.com/bob/tools.git")
	if got, code := f.gitCredential(bobs, "run-widgets"); (code != http.StatusForbidden && code != http.StatusNotFound) ||
		got.Kind != "" || got.Error == "no_source_credential" {
		t.Fatalf("another team's runner = %d %+v, want a refusal that reveals nothing", code, got)
	}
}
