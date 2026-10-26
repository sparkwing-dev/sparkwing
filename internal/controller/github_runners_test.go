package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/githuboidc/githuboidctest"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth/googletest"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
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
	google, gh := googletest.New(t), githuboidctest.New(t)
	var srv *controller.Server
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"), store.WithPolicies(func(st *store.Store) (store.Policies, error) {
		srv = controller.NewDeployment(st, deploymenttest.Parse(t, deployment.Inputs{Kind: string(deployment.KindTeam), PublicOrigin: ghAudience}, license.FeatureMultiTeam), nil).WithSecretsCipher(testCipher(t)).
			WithGoogleSignIn(googleauth.New(google.Config()), []string{dashRedirect}).
			WithGitHubRunners(gh.Config(ghAudience)).
			WithDashboard(controller.Dashboard{})
		configureGitHubClaims(t, srv, st)
		policies := srv.StorePolicies()
		policies.ClaimProfiles = githubClaimEnvironment{}
		return policies, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	admin, _, err := teststore.ProvisionCredential(context.Background(), st, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, Team: "", Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "root"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}, ExpiresAt: teststore.CredentialExpiry(time.Now().UTC(), 0)})
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &ghFixture{
		identityFixture: &identityFixture{t: t, url: ts.URL, store: st, google: google, admin: admin, srv: srv},
		gh:              gh,
	}
}

func (f *ghFixture) bind(owner signedIn, job githuboidctest.Job) {
	f.t.Helper()
	if code := f.call("POST", "/api/v1/runners/github", owner.auth, map[string]any{
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
	id := int64(42)
	if strings.EqualFold(slug, "acme/gadgets") {
		id = 43
	}
	seedSourceRun(f.t, f.store, team, runID, "https://github.com/"+slug+".git", githubFixtureKey(id), "refs/heads/"+branch, sha, id, runspec.DispatchHost)
	if err := teststore.SeedNode(f.store, ctx, store.Node{RunID: runID, NodeID: "compile", Status: "pending"}); err != nil {
		f.t.Fatal(err)
	}
	if err := teststore.SeedNodeReady(f.store, ctx, runID, "compile"); err != nil {
		f.t.Fatal(err)
	}
}

func TestGitHubRunnerCredentialClaimsOnlyItsRepository(t *testing.T) {
	f := newGHFixture(t)
	owner, stranger := f.user("o", "olga@example.com"), f.user("s", "sam@example.com")
	f.bind(owner, widgetsJob)
	f.work(owner.team, "run-gadgets", "acme/gadgets")
	f.work(stranger.team, "run-strangers-widgets", "acme/widgets")
	runner := f.credential(widgetsJob, owner.team)
	for _, kind := range []capability.UnitKind{capability.UnitNode, capability.UnitTrigger} {
		var response controllerwire.ClaimResponse
		if code := f.call("POST", "/api/v1/runners/claim", runner, githubClaimBody(kind, "", ""), &response); code != http.StatusOK || response.Claim != nil {
			t.Fatalf("foreign queue: HTTP %d %+v", code, response)
		}
		for _, run := range []string{"run-gadgets", "run-strangers-widgets"} {
			job := ""
			if kind == capability.UnitNode {
				job = "compile"
			}
			if code := f.call("POST", "/api/v1/runners/claim", runner, githubClaimBody(kind, run, job), nil); code != http.StatusForbidden {
				t.Fatalf("named %s %s = %d, want 403", kind, run, code)
			}
		}
	}
	if code := f.call("GET", "/api/v1/runs/run-gadgets", runner, nil, nil); code != http.StatusForbidden {
		t.Fatalf("read another repository = %d", code)
	}
	f.work(owner.team, "run-widgets", "Acme/Widgets")
	var response controllerwire.ClaimResponse
	if code := f.call("POST", "/api/v1/runners/claim", runner, githubClaimBody(capability.UnitNode, "", ""), &response); code != http.StatusOK || response.Claim == nil || response.Claim.RunID != "run-widgets" {
		t.Fatalf("own queue = %d %+v", code, response)
	}
	if code := f.call("GET", "/api/v1/runs/run-widgets", "Bearer "+response.Claim.Capability, nil, nil); code != http.StatusOK {
		t.Fatalf("read own run = %d", code)
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
		{"GET", "/api/v1/runners/github"},
		{"POST", "/api/v1/tokens"},
		{"GET", "/api/v1/tokens"},
	} {
		if code := f.call(route[0], route[1], runner, map[string]any{}, nil); code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", route[0], route[1], code)
		}
	}
}

func TestGitHubRunnerCredentialsPerTeamAreBounded(t *testing.T) {
	f := newGHFixture(t)
	owner := f.user("o", "olga@example.com")
	f.bind(owner, widgetsJob)
	for i := range 20 {
		job := widgetsJob
		job.RunID = fmt.Sprint(1000 + i)
		if _, code := f.exchange(f.gh.Token(job, ghAudience), owner.team); code != http.StatusCreated {
			t.Fatalf("mint %d: HTTP %d", i, code)
		}
	}

	if _, code := f.exchange(f.gh.Token(widgetsJob, ghAudience), owner.team); code != http.StatusTooManyRequests {
		t.Fatalf("21st credential = %d, want 429", code)
	}
}

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
	for _, kind := range []capability.UnitKind{capability.UnitNode, capability.UnitTrigger} {
		var response controllerwire.ClaimResponse
		if code := f.call("POST", "/api/v1/runners/claim", runner, githubClaimBody(kind, "", ""), &response); code != http.StatusOK || response.Claim != nil {
			t.Fatalf("other pushes queued = %d %+v", code, response)
		}
		for _, run := range []string{"run-main", "run-feature-old"} {
			job := ""
			if kind == capability.UnitNode {
				job = "compile"
			}
			if code := f.call("POST", "/api/v1/runners/claim", runner, githubClaimBody(kind, run, job), nil); code != http.StatusForbidden {
				t.Fatalf("named %s %s = %d", kind, run, code)
			}
			if code := f.call("GET", "/api/v1/runs/"+run, runner, nil, nil); code != http.StatusForbidden {
				t.Fatalf("read %s = %d", run, code)
			}
		}
	}
	f.workAt(owner.team, "run-feature", "Acme/Widgets", "feature", featureSHA)
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		var response controllerwire.ClaimResponse
		if code := f.call("POST", "/api/v1/runners/claim", runner, githubClaimBody(kind, "", ""), &response); code != http.StatusOK || response.Claim == nil || response.Claim.RunID != "run-feature" {
			t.Fatalf("own %s = %d %+v", kind, code, response)
		}
	}
}

// The exchange is unauthenticated until GitHub's token verifies, so it reads no
// more than a token request needs.
func TestGitHubRunnerExchangeReadsOnlyATokenSizedBody(t *testing.T) {
	f := newGHFixture(t)
	owner := f.user("o", "olga@example.com")
	f.bind(owner, widgetsJob)
	body, err := json.Marshal(map[string]string{"id_token": f.gh.Token(widgetsJob, ghAudience), "team": owner.team})
	if err != nil {
		t.Fatal(err)
	}
	for _, padding := range []int{0, 64 << 10} {
		request, err := http.NewRequest("POST", f.url+"/api/v1/runners/github/exchange", bytes.NewReader(append(body, bytes.Repeat([]byte(" "), padding)...)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if want := map[bool]int{true: http.StatusCreated, false: http.StatusBadRequest}[padding == 0]; response.StatusCode != want {
			t.Fatalf("padding %d: HTTP %d, want %d", padding, response.StatusCode, want)
		}
	}
}
