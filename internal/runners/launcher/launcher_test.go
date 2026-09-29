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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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

func newLaunchFixture(t *testing.T, wrap ...func(http.Handler) http.Handler) launchFixture {
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
	h := controller.New(st, nil).EnableAuthFromStore().Handler()
	for _, w := range wrap {
		h = w(h)
	}
	srv := httptest.NewServer(h)
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
		`{"dispatch":"controller"}`); code != http.StatusOK {
		t.Fatalf("opting in answered %d, want 200", code)
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

func (f launchFixture) optedInRun(t *testing.T, runID string) {
	t.Helper()
	tn, err := f.st.ForTeam(context.Background(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.SetRepoDispatch(context.Background(), "korey", "probe", store.RepoDispatchController, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.intake(t, runID, "korey/probe")
}

func (f launchFixture) unplacedPod(t *testing.T, job string, waited time.Duration) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: job + "-pod", Namespace: "sparkwing-jobs", Labels: map[string]string{
			"app.kubernetes.io/managed-by": "sparkwing-launcher", launcher.JobLabel: job,
		}},
		Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
			LastTransitionTime: metav1.NewTime(time.Now().Add(-waited)),
		}}},
	}
	if _, err := f.kube.CoreV1().Pods("sparkwing-jobs").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func (f launchFixture) jobNames(t *testing.T) []string {
	t.Helper()
	jobs, err := f.kube.BatchV1().Jobs("sparkwing-jobs").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, j := range jobs.Items {
		names = append(names, j.Name)
	}
	return names
}

// A Job that has waited past the release point for a machine is deleted and
// its node goes back to the queue unbilled, marked as waiting; the launcher
// then reports it has no capacity. A Job that just started waiting stays.
func TestSync_HandsBackAJobThatNeverGotAMachine(t *testing.T) {
	ctx := context.Background()
	f := newLaunchFixture(t)
	f.optedInRun(t, "run-wait")
	l := f.launcher(f.token(t, controller.ScopeClaimsLaunch))
	if launched, err := l.LaunchOne(ctx); !launched || err != nil {
		t.Fatalf("launch: %v %v", launched, err)
	}
	job := f.jobNames(t)[0]
	f.unplacedPod(t, job, 30*time.Second)
	if reason, err := l.Sync(ctx); err != nil || reason != "" {
		t.Fatalf("sync of a fresh Job = %q, %v; want capacity", reason, err)
	}
	if names := f.jobNames(t); len(names) != 1 {
		t.Fatalf("a Job waiting 30s was deleted: %v", names)
	}
	if err := f.kube.CoreV1().Pods("sparkwing-jobs").Delete(ctx, job+"-pod", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	f.unplacedPod(t, job, launcher.UnschedulableRelease+time.Second)
	reason, err := l.Sync(ctx)
	if err != nil || reason == "" {
		t.Fatalf("sync of a stuck Job = %q, %v; want a capacity wait", reason, err)
	}
	if names := f.jobNames(t); len(names) != 0 {
		t.Fatalf("the stuck Job was kept: %v", names)
	}
	n, err := f.st.GetNode(ctx, "run-wait", store.PlanNodeID)
	if err != nil || n.ClaimedBy != "" || n.AttemptsConsumed != 0 || n.StatusDetail != store.CapacityWaitDetail {
		t.Fatalf("node after release = %+v %v", n, err)
	}
}

// A cancelled run's Job is deleted on the next sync, which is what stops a
// pod that is not heartbeating.
func TestSync_DeletesACancelledRunsJob(t *testing.T) {
	ctx := context.Background()
	f := newLaunchFixture(t)
	f.optedInRun(t, "run-cancel")
	l := f.launcher(f.token(t, controller.ScopeClaimsLaunch))
	if launched, err := l.LaunchOne(ctx); !launched || err != nil {
		t.Fatalf("launch: %v %v", launched, err)
	}
	if _, err := l.Sync(ctx); err != nil || len(f.jobNames(t)) != 1 {
		t.Fatalf("sync of a live claim: %v, jobs %v", err, f.jobNames(t))
	}
	if err := f.st.RequestCancel(ctx, "run-cancel"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if names := f.jobNames(t); len(names) != 0 {
		t.Fatalf("the cancelled run's Job survived: %v", names)
	}
}

// With no capacity the launcher's sync says so: the node stays queued in the
// controller, unclaimed and unbilled, and says why; with room it is claimed.
func TestSync_ReportsAFullPoolAndLeavesTheNodeQueued(t *testing.T) {
	f := newLaunchFixture(t)
	f.optedInRun(t, "run-full")
	l := f.launcher(f.token(t, controller.ScopeClaimsLaunch))
	pool := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1", "kind": "NodePool", "metadata": map[string]any{"name": "jobs"},
		"spec":   map[string]any{"limits": map[string]any{"cpu": "80"}},
		"status": map[string]any{"resources": map[string]any{"cpu": "78"}},
	}}
	l.Capacity = launcher.NodePoolCapacity(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), pool), "jobs")
	ctx := context.Background()
	if reason, err := l.Sync(ctx); err != nil || reason == "" {
		t.Fatalf("sync with the pool full = %q, %v; want a capacity wait", reason, err)
	}
	n, err := f.st.GetNode(ctx, "run-full", store.PlanNodeID)
	if err != nil || n.ClaimedBy != "" || n.StatusDetail != store.CapacityWaitDetail {
		t.Fatalf("queued node = %+v %v", n, err)
	}
	pool.Object["status"] = map[string]any{"resources": map[string]any{"cpu": "72"}}
	l.Capacity = launcher.NodePoolCapacity(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), pool), "jobs")
	if reason, err := l.Sync(ctx); err != nil || reason != "" {
		t.Fatalf("sync with 8 cores free = %q, %v; want capacity", reason, err)
	}
	if launched, err := l.LaunchOne(ctx); !launched || err != nil {
		t.Fatalf("launch with room: %v %v", launched, err)
	}
}
