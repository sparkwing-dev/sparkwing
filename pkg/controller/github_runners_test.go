package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githuboidc/githuboidctest"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth/googletest"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

const ghAudience = "https://ci.example.com"

var (
	widgetsJob = githuboidctest.Job{Repository: "Acme/Widgets", RepositoryID: 42, RepositoryOwnerID: 7, RunID: "555"}
	gadgetsJob = githuboidctest.Job{Repository: "acme/gadgets", RepositoryID: 43, RepositoryOwnerID: 7}
)

type ghFixture struct {
	*identityFixture
	gh *githuboidctest.Issuer
}

func newGHFixture(t *testing.T) *ghFixture {
	t.Helper()
	raw, pub := multiTeamLicense(t)
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	admin, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	google, gh := googletest.New(t), githuboidctest.New(t)
	srv := controller.New(st, nil).EnableAuthFromStore().
		WithLicense(license.Resolve(raw, pub, time.Now(), nil)).
		WithGoogleSignIn(googleauth.New(google.Config()), []string{dashRedirect}).
		WithExternalURL(ghAudience).
		WithGitHubRunners(gh.Config(ghAudience)).
		WithDashboard(controller.Dashboard{})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return &ghFixture{
		identityFixture: &identityFixture{t: t, url: ts.URL, store: st, google: google, admin: admin},
		gh:              gh,
	}
}

func (f *ghFixture) bind(owner signedIn, job githuboidctest.Job) {
	f.t.Helper()
	if code := f.call("POST", "/api/v1/team/github-runners", owner.auth, map[string]any{
		"repository": job.Repository, "repository_id": job.RepositoryID, "repository_owner_id": job.RepositoryOwnerID,
	}, nil); code != http.StatusCreated {
		f.t.Fatalf("bind %s = %d", job.Repository, code)
	}
}

type ghCredential struct {
	Token      string   `json:"token"`
	Team       string   `json:"team"`
	Repository string   `json:"repository"`
	ExpiresAt  int64    `json:"expires_at"`
	Labels     []string `json:"labels"`
}

func (f *ghFixture) exchange(idToken, team string) (ghCredential, int) {
	f.t.Helper()
	var out ghCredential
	code := f.call("POST", "/api/v1/runners/github/exchange", "", map[string]string{"id_token": idToken, "team": team}, &out)
	return out, code
}

func (f *ghFixture) credential(job githuboidctest.Job, team string) string {
	f.t.Helper()
	cred, code := f.exchange(f.gh.Token(job, ghAudience), team)
	if code != http.StatusCreated {
		f.t.Fatalf("exchange for %s = %d", job.Repository, code)
	}
	return "Bearer " + cred.Token
}

func (f *ghFixture) work(team, runID, slug string) {
	f.t.Helper()
	f.workAt(team, runID, slug, "main", githuboidctest.DefaultSHA)
}

func (f *ghFixture) workAt(team, runID, slug, branch, sha string) {
	f.t.Helper()
	ctx := context.Background()
	tn, err := f.store.ForTeam(ctx, store.Team(team))
	if err != nil {
		f.t.Fatal(err)
	}
	owner, name, _ := strings.Cut(slug, "/")
	now := time.Now()
	if err := tn.CreateTriggerWithRun(ctx, store.Trigger{
		ID: runID, Pipeline: "build", TriggerSource: "github", Repo: slug,
		GithubOwner: owner, GithubRepo: name, TriggerEnv: map[string]string{"GITHUB_EVENT_NAME": "push"}, CreatedAt: now,
		WebhookDelivery: runID, WebhookReplayKey: "signed-" + runID,
		GitBranch: branch, GitSHA: sha,
	}, store.Run{
		ID: runID, Pipeline: "build", Status: "pending", DeclaredRepo: slug, GithubOwner: owner, GithubRepo: name,
		CreatedAt: now, StartedAt: now, GitBranch: branch, GitSHA: sha,
	}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.CreateNode(ctx, store.Node{RunID: runID, NodeID: "compile", Status: "pending"}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.MarkNodeReady(ctx, runID, "compile"); err != nil {
		f.t.Fatal(err)
	}
}

func TestGitHubRunnerCredentialClaimsOnlyItsRepository(t *testing.T) {
	f := newGHFixture(t)
	owner := f.user("o", "olga@example.com")
	stranger := f.user("s", "sam@example.com")
	f.bind(owner, widgetsJob)
	f.work(owner.team, "run-gadgets", "acme/gadgets")
	f.work(stranger.team, "run-strangers-widgets", "acme/widgets")
	runner := f.credential(widgetsJob, owner.team)
	claim := map[string]any{"holder_id": "gh-1", "labels": []string{controller.GitHubActionsLabel}}

	if code := f.call("POST", "/api/v1/nodes/claim", runner, claim, nil); code != http.StatusNoContent {
		t.Fatalf("claim with only other repositories' and teams' work queued = %d, want 204", code)
	}
	for _, path := range []string{
		"/api/v1/runs/run-gadgets/nodes/compile/claim",
		"/api/v1/runs/run-strangers-widgets/nodes/compile/claim",
	} {
		if code := f.call("POST", path, runner, map[string]any{"holder_id": "gh-1"}, nil); code != http.StatusForbidden {
			t.Fatalf("named claim %s = %d, want 403", path, code)
		}
	}
	if code := f.call("GET", "/api/v1/runs/run-gadgets", runner, nil, nil); code != http.StatusForbidden {
		t.Fatalf("read another repository's run = %d, want 403", code)
	}
	if code := f.call("POST", "/api/v1/triggers/claim", runner, nil, nil); code != http.StatusNoContent {
		t.Fatalf("trigger claim with only other repositories' triggers = %d, want 204", code)
	}
	if code := f.call("POST", "/api/v1/triggers/run-gadgets/claim", runner, nil, nil); code != http.StatusForbidden {
		t.Fatalf("claiming another repository's trigger by id = %d, want 403", code)
	}

	f.work(owner.team, "run-widgets", "Acme/Widgets")
	var node struct {
		RunID  string `json:"run_id"`
		NodeID string `json:"node_id"`
	}
	if code := f.call("POST", "/api/v1/nodes/claim", runner, claim, &node); code != http.StatusOK || node.RunID != "run-widgets" {
		t.Fatalf("claim = %d %+v, want run-widgets", code, node)
	}
	if code := f.call("GET", "/api/v1/runs/run-widgets", runner, nil, nil); code != http.StatusOK {
		t.Fatalf("read its own run = %d, want 200", code)
	}
}

func TestGitHubRunnerCredentialReachesNothingOutsideItsRoutes(t *testing.T) {
	f := newGHFixture(t)
	owner := f.user("o", "olga@example.com")
	f.bind(owner, widgetsJob)
	f.work(owner.team, "run-widgets", "acme/widgets")
	runner := f.credential(widgetsJob, owner.team)
	for _, route := range [][2]string{
		{"GET", "/api/v1/runs"},
		{"GET", "/api/v1/team/github-runners"},
		{"POST", "/api/v1/team/tokens"},
		{"GET", "/api/v1/tokens"},
	} {
		if code := f.call(route[0], route[1], runner, map[string]any{}, nil); code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", route[0], route[1], code)
		}
	}
}

func TestGitHubRunnerBindingsAreOwnerOnlyAndUnbindingRevokes(t *testing.T) {
	f := newGHFixture(t)
	owner, editor, reader := teamOf(f.identityFixture)
	for _, who := range []signedIn{editor, reader} {
		if code := f.call("POST", "/api/v1/team/github-runners", who.auth, map[string]any{
			"repository": "acme/widgets", "repository_id": 42, "repository_owner_id": 7,
		}, nil); code != http.StatusForbidden {
			t.Fatalf("non-owner bind = %d, want 403", code)
		}
	}
	f.bind(owner, widgetsJob)
	var listed struct {
		Bindings []struct {
			Repository   string `json:"repository"`
			RepositoryID int64  `json:"repository_id"`
		} `json:"bindings"`
		Workflow string `json:"workflow"`
	}
	if code := f.call("GET", "/api/v1/team/github-runners", reader.auth, nil, &listed); code != http.StatusOK ||
		len(listed.Bindings) != 1 || listed.Bindings[0].RepositoryID != 42 {
		t.Fatalf("list = %d %+v", code, listed)
	}
	if !strings.Contains(listed.Workflow, "--team "+owner.team) || !strings.Contains(listed.Workflow, "--controller "+ghAudience) ||
		!strings.Contains(listed.Workflow, "id-token: write") {
		t.Fatalf("workflow does not name the team, controller and token permission:\n%s", listed.Workflow)
	}
	runner := f.credential(widgetsJob, owner.team)
	if code := f.call("DELETE", "/api/v1/team/github-runners/42", editor.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("editor unbind = %d, want 403", code)
	}
	if code := f.call("DELETE", "/api/v1/team/github-runners/42", owner.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("unbind = %d", code)
	}
	if code := f.call("GET", "/api/v1/auth/whoami", runner, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("credential after unbind = %d, want 401", code)
	}
	if _, code := f.exchange(f.gh.Token(widgetsJob, ghAudience), owner.team); code != http.StatusForbidden {
		t.Fatalf("exchange after unbind = %d, want 403", code)
	}
}

func TestGitHubRunnerCredentialsPerTeamAreBounded(t *testing.T) {
	f := newGHFixture(t)
	owner := f.user("o", "olga@example.com")
	f.bind(owner, widgetsJob)
	tn, err := f.store.ForTeam(context.Background(), store.Team(owner.team))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if _, _, err := tn.CreateToken(context.Background(), "github:42:Acme/Widgets", store.TokenKindRunner,
			[]string{controller.ScopeNodesClaim}, time.Hour, time.Now()); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
	if _, code := f.exchange(f.gh.Token(widgetsJob, ghAudience), owner.team); code != http.StatusTooManyRequests {
		t.Fatalf("21st credential = %d, want 429", code)
	}
}

// A job's ID token names the push it runs for. Its credential reaches only
// work recorded for that same branch and commit, so a workflow on a feature
// branch cannot claim main's runs or read the secrets they are given.
func TestGitHubRunnerCredentialClaimsOnlyItsOwnPush(t *testing.T) {
	f := newGHFixture(t)
	owner := f.user("o", "olga@example.com")
	f.bind(owner, widgetsJob)
	const featureSHA = "fedcba9876543210fedcba9876543210fedcba98"
	feature := widgetsJob
	feature.Ref, feature.SHA = "refs/heads/feature", featureSHA
	f.workAt(owner.team, "run-main", "Acme/Widgets", "main", githuboidctest.DefaultSHA)
	f.workAt(owner.team, "run-feature-old", "Acme/Widgets", "feature", githuboidctest.DefaultSHA)
	runner := f.credential(feature, owner.team)
	claim := map[string]any{"holder_id": "gh-1", "labels": []string{controller.GitHubActionsLabel}}

	if code := f.call("POST", "/api/v1/nodes/claim", runner, claim, nil); code != http.StatusNoContent {
		t.Fatalf("claim with only main's and an older feature commit's work queued = %d, want 204", code)
	}
	if code := f.call("POST", "/api/v1/triggers/claim", runner, nil, nil); code != http.StatusNoContent {
		t.Fatalf("trigger claim with only other pushes' triggers = %d, want 204", code)
	}
	for _, run := range []string{"run-main", "run-feature-old"} {
		if code := f.call("POST", "/api/v1/runs/"+run+"/nodes/compile/claim", runner, map[string]any{"holder_id": "gh-1"}, nil); code != http.StatusForbidden {
			t.Errorf("named claim on %s = %d, want 403", run, code)
		}
		if code := f.call("GET", "/api/v1/runs/"+run, runner, nil, nil); code != http.StatusForbidden {
			t.Errorf("read %s = %d, want 403", run, code)
		}
		if code := f.call("POST", "/api/v1/triggers/"+run+"/claim", runner, nil, nil); code != http.StatusForbidden {
			t.Errorf("claim trigger %s by id = %d, want 403", run, code)
		}
	}

	f.workAt(owner.team, "run-feature", "Acme/Widgets", "feature", featureSHA)
	var trigger store.Trigger
	if code := f.call("POST", "/api/v1/triggers/claim", runner, nil, &trigger); code != http.StatusOK || trigger.ID != "run-feature" {
		t.Fatalf("claim own push's trigger = %d %+v, want run-feature", code, trigger)
	}
	var node struct {
		RunID string `json:"run_id"`
	}
	if code := f.call("POST", "/api/v1/nodes/claim", runner, claim, &node); code != http.StatusOK || node.RunID != "run-feature" {
		t.Fatalf("claim = %d %+v, want run-feature", code, node)
	}
}

func TestGitHubRunnerWorkflowDocMatchesTheRenderedTemplate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "github-actions-runners.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.ReplaceAll(string(raw), "\r\n", "\n")
	want := controller.GitHubRunnerWorkflow("https://sparkwing.example.com", "acme")
	if !strings.Contains(doc, want) {
		t.Fatalf("docs/github-actions-runners.md does not carry the workflow the controller renders:\n%s", want)
	}
	if !strings.Contains(doc, controller.GitHubActionsLabel) {
		t.Fatal("doc does not name the runner label")
	}
}
