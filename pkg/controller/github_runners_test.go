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
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
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
		WithGitHubRunners(gh.Config(ghAudience))
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

// work writes a pending trigger, its run and a ready node for slug into team,
// the way a push webhook records a push of main at the default commit.
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
		ID: runID, Pipeline: "build", Repo: slug, GithubOwner: owner, GithubRepo: name, CreatedAt: now,
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

func TestGitHubRunnerExchangeNeedsBothSidesOfConsent(t *testing.T) {
	f := newGHFixture(t)
	owner := f.user("o", "olga@example.com")
	stranger := f.user("s", "sam@example.com")
	f.bind(owner, widgetsJob)

	cred, code := f.exchange(f.gh.Token(widgetsJob, ghAudience), owner.team)
	if code != http.StatusCreated || cred.Team != owner.team || cred.Repository != "Acme/Widgets" ||
		len(cred.Labels) != 1 || cred.Labels[0] != controller.GitHubActionsLabel {
		t.Fatalf("exchange = %d %+v", code, cred)
	}
	if left := time.Until(time.Unix(cred.ExpiresAt, 0)); left <= 0 || left > time.Hour {
		t.Fatalf("credential lives %s, want at most an hour", left)
	}
	w := f.whoami("Bearer " + cred.Token)
	if w.Team != owner.team || w.Principal != "github:42:Acme/Widgets" {
		t.Fatalf("whoami = %+v", w)
	}
	token, err := f.store.LookupToken(cred.Token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := f.store.ForTeam(context.Background(), store.Team(owner.team))
	if err != nil {
		t.Fatal(err)
	}
	push, err := tenant.GitHubRunnerCredentialPush(context.Background(), token.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	if push.RunID != widgetsJob.RunID {
		t.Fatalf("credential workflow run = %q, want %q", push.RunID, widgetsJob.RunID)
	}

	transferred := widgetsJob
	transferred.RepositoryOwnerID = 8
	past := time.Now().Add(-time.Hour).Unix()
	refusals := map[string]struct {
		token, team string
		want        int
	}{
		"repository the team never bound":       {f.gh.Token(gadgetsJob, ghAudience), owner.team, http.StatusForbidden},
		"team that never bound the repository":  {f.gh.Token(widgetsJob, ghAudience), stranger.team, http.StatusForbidden},
		"team that does not exist":              {f.gh.Token(widgetsJob, ghAudience), "nobody", http.StatusForbidden},
		"no team named":                         {f.gh.Token(widgetsJob, ghAudience), "", http.StatusForbidden},
		"repository transferred to a new owner": {f.gh.Token(transferred, ghAudience), owner.team, http.StatusForbidden},
		"token for another audience":            {f.gh.Token(widgetsJob, "https://other.example.com"), owner.team, http.StatusUnauthorized},
		"expired token":                         {f.gh.TokenWith(widgetsJob, ghAudience, map[string]any{"exp": past}), owner.team, http.StatusUnauthorized},
		"token from an unpublished key":         {f.gh.TokenSignedBy(f.gh.ForeignKey(), widgetsJob, ghAudience), owner.team, http.StatusUnauthorized},
		"another issuer":                        {f.gh.TokenWith(widgetsJob, ghAudience, map[string]any{"iss": "https://evil.example.com"}), owner.team, http.StatusUnauthorized},
	}
	for name, c := range refusals {
		t.Run(name, func(t *testing.T) {
			if got, code := f.exchange(c.token, c.team); code != c.want || got.Token != "" {
				t.Fatalf("exchange = %d %+v, want %d and no credential", code, got, c.want)
			}
		})
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
	offer := map[string]any{
		"holder_id": "gh-1", "executor_name": "x", "run_id": "run-widgets", "node_id": "compile",
		"reservation_id": "r", "resource_digest": "d", "slot": 0,
	}
	if code := f.call("POST", "/api/v1/nodes/claim", runner, offer, nil); code != http.StatusForbidden {
		t.Fatalf("executor offer = %d, want 403", code)
	}
	for _, route := range [][2]string{
		{"GET", "/api/v1/runs"},
		{"POST", "/api/v1/nodes/claim/prepare"},
		{"GET", "/api/v1/team/github-runners"},
		{"POST", "/api/v1/team/runner-tokens"},
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
	var node struct {
		RunID string `json:"run_id"`
	}
	if code := f.call("POST", "/api/v1/nodes/claim", runner, claim, &node); code != http.StatusOK || node.RunID != "run-feature" {
		t.Fatalf("claim = %d %+v, want run-feature", code, node)
	}
}

// A pull_request or pull_request_target job runs code or input the
// repository's owners did not push, so it gets no credential; neither does a
// job for anything but a branch push.
func TestGitHubRunnerExchangeRefusesAnythingButABranchPush(t *testing.T) {
	f := newGHFixture(t)
	owner := f.user("o", "olga@example.com")
	f.bind(owner, widgetsJob)
	refusals := map[string]githuboidctest.Job{}
	for _, event := range []string{"pull_request", "pull_request_target"} {
		job := widgetsJob
		job.EventName, job.Ref = event, "refs/pull/7/merge"
		refusals[event] = job
		onBranch := widgetsJob
		onBranch.EventName = event
		refusals[event+" naming a branch ref"] = onBranch
	}
	tag := widgetsJob
	tag.Ref = "refs/tags/v1.0.0"
	refusals["tag push"] = tag
	for name, job := range refusals {
		t.Run(name, func(t *testing.T) {
			if got, code := f.exchange(f.gh.Token(job, ghAudience), owner.team); code != http.StatusForbidden || got.Token != "" {
				t.Fatalf("exchange = %d %+v, want 403 and no credential", code, got)
			}
		})
	}
	noSHA := f.gh.TokenWith(widgetsJob, ghAudience, map[string]any{"sha": nil})
	if got, code := f.exchange(noSHA, owner.team); code/100 == 2 || got.Token != "" {
		t.Fatalf("exchange without a sha claim = %d %+v, want a refusal", code, got)
	}
	dispatch := widgetsJob
	dispatch.EventName = "workflow_dispatch"
	if _, code := f.exchange(f.gh.Token(dispatch, ghAudience), owner.team); code != http.StatusCreated {
		t.Fatalf("workflow_dispatch on a branch = %d, want 201", code)
	}
}

func TestGitHubRunnerWorkflowDocMatchesTheRenderedTemplate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "github-actions-runners.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	want := controller.GitHubRunnerWorkflow("https://sparkwing.example.com", "acme")
	if !strings.Contains(doc, want) {
		t.Fatalf("docs/github-actions-runners.md does not carry the workflow the controller renders:\n%s", want)
	}
	if !strings.Contains(doc, controller.GitHubActionsLabel) {
		t.Fatal("doc does not name the runner label")
	}
}
