package launcher_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type launchFixture struct {
	st   *store.Store
	url  string
	kube *fake.Clientset
}

func newLaunchFixture(t *testing.T) launchFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// safety: a controller whose tokens table is empty serves unauthenticated,
	// so one token exists before it starts.
	if _, _, err := st.CreateToken("operator", store.TokenKindService, []string{controller.ScopeAdmin}, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	return launchFixture{st: st, url: srv.URL, kube: fake.NewSimpleClientset()}
}

func (f launchFixture) token(t *testing.T, scopes ...string) string {
	t.Helper()
	raw, _, err := f.st.CreateToken("launcher", store.TokenKindService, scopes, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f launchFixture) intake(t *testing.T, runID, repo string) {
	t.Helper()
	now := time.Now()
	owner, name, _ := strings.Cut(repo, "/")
	if err := f.st.CreateTriggerWithRun(context.Background(), store.Trigger{
		ID: runID, Pipeline: "demo", GithubOwner: owner, GithubRepo: name, CreatedAt: now,
	}, store.Run{ID: runID, Pipeline: "demo", Status: "pending", GithubOwner: owner, GithubRepo: name, CreatedAt: now, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func (f launchFixture) launcher(bearer string) *launcher.Launcher {
	cfg := launcher.Config{
		Namespace: "sparkwing-jobs", ControllerURL: "http://controller",
		Image:      "registry/sparkwing-runner@sha256:" + strings.Repeat("a", 64),
		CPUCeiling: 8, MemoryCeiling: 16 << 30, Deadline: time.Hour,
	}
	return &launcher.Launcher{
		Kube: f.kube, Ctrl: client.NewWithToken(f.url, nil, bearer), Config: cfg,
		Holder: "launcher:test", Poll: time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func (f launchFixture) request(t *testing.T, method, path, bearer string) int {
	t.Helper()
	return f.requestBody(t, method, path, bearer, `{}`)
}

func (f launchFixture) requestBody(t *testing.T, method, path, bearer, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, f.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// An opted-in repository's run becomes one Job carrying one claim token, and
// the launcher does nothing else: it creates the Job and runs nothing itself.
// The opted-out repository's run stays on the trigger path.
func TestLaunchOne_RunsAnOptedInRepoAsOneJobWithAClaimToken(t *testing.T) {
	ctx := context.Background()
	f := newLaunchFixture(t)
	admin := f.token(t, controller.ScopeAdmin)
	if code := f.requestBody(t, http.MethodPut, "/api/v1/teams/default/repos/korey/probe/dispatch", admin,
		`{"dispatch":"shell"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown dispatch answered %d, want 400", code)
	}
	if code := f.requestBody(t, http.MethodPut, "/api/v1/teams/default/repos/korey/probe/dispatch", admin,
		`{"dispatch":"controller"}`); code != http.StatusConflict {
		t.Fatalf("opting in before the path is complete answered %d, want 409", code)
	}
	// safety: the setter refuses controller dispatch until the path can run, so
	// the test writes the row it will write once it accepts it.
	if _, err := f.st.DB().ExecContext(ctx, `INSERT INTO repos (team, repo, dispatch, updated_at) VALUES (?, ?, ?, ?)`,
		string(store.DefaultTeam), store.RepoKey("korey", "probe"), string(store.RepoDispatchController), time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	f.intake(t, "run-old", "korey/other")
	f.intake(t, "run-new", "korey/probe")

	l := f.launcher(f.token(t, controller.ScopeClaimsLaunch))
	if launched, err := l.LaunchOne(ctx); !launched || err != nil {
		t.Fatalf("launch: %v %v", launched, err)
	}
	if launched, err := l.LaunchOne(ctx); launched || err != nil {
		t.Fatalf("second launch took %v (%v); the opted-out run is not the launcher's", launched, err)
	}
	actions := f.kube.Actions()
	if len(actions) != 1 || actions[0].GetVerb() != "create" || actions[0].GetResource().Resource != "jobs" {
		t.Fatalf("kubernetes actions = %v, want one Job create", actions)
	}
	jobs, err := f.kube.BatchV1().Jobs("sparkwing-jobs").List(ctx, metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 {
		t.Fatalf("jobs = %v %v", jobs, err)
	}
	c := jobs.Items[0].Spec.Template.Spec.Containers[0]
	if strings.Join(c.Args, " ") != "run-node run-new "+store.PlanNodeID {
		t.Fatalf("job args = %v", c.Args)
	}
	var token string
	for _, e := range c.Env {
		if e.Name == "SPARKWING_AGENT_TOKEN" {
			token = e.Value
		}
	}
	tok, err := f.st.AuthorizeClaimToken(ctx, token, store.ClaimSensitive, time.Now())
	if err != nil || tok.RunID != "run-new" || tok.NodeID != store.PlanNodeID {
		t.Fatalf("job token authorizes %+v, %v", tok, err)
	}
	for _, path := range []string{"/api/v1/tokens", "/api/v1/launcher/claim", "/api/v1/runs/run-old/plan"} {
		if code := f.request(t, http.MethodPost, path, token); code < 400 || code >= 500 {
			t.Errorf("the job's claim token reached POST %s: %d", path, code)
		}
	}
}

func TestLaunchClaim_RefusesEveryCredentialButTheLauncher(t *testing.T) {
	f := newLaunchFixture(t)
	f.intake(t, "run-x", "korey/other")
	for name, scopes := range map[string][]string{
		"runner pool": {controller.ScopeNodesClaim, controller.ScopeTriggersClaim, controller.ScopeRunsState},
		"team owner":  controller.ScopesForRole(store.RoleOwner),
	} {
		if code := f.request(t, http.MethodPost, "/api/v1/launcher/claim", f.token(t, scopes...)); code != http.StatusForbidden {
			t.Errorf("%s: launch claim answered %d, want 403", name, code)
		}
		if code := f.request(t, http.MethodPut, "/api/v1/teams/default/repos/korey/other/dispatch", f.token(t, scopes...)); code != http.StatusForbidden {
			t.Errorf("%s: repo dispatch answered %d, want 403", name, code)
		}
	}
}

// A controller that answers a token too short to outlast the margin gets no
// Job for it: the launcher leaves the claim to lapse.
func TestLaunchOne_CreatesNoJobForATokenUnderTheFloor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"team":"alpha","run_id":"run-1","node_id":"build","generation":1,"kind":"work","token":"swc_x","lifetime_secs":30}`)
	}))
	t.Cleanup(srv.Close)
	f := launchFixture{kube: fake.NewSimpleClientset(), url: srv.URL}
	launched, err := f.launcher("swr_launch").LaunchOne(context.Background())
	if !launched || err == nil {
		t.Fatalf("LaunchOne = %v, %v; want the short claim refused", launched, err)
	}
	if actions := f.kube.Actions(); len(actions) != 0 {
		t.Fatalf("kubernetes actions = %v, want none for a claim under the floor", actions)
	}
}
