//go:build unix

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/runflags"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// perf: each test links its own copy into its temp directory, which the build cache keeps cheap, so no
// binary outlives the test.
func podFixture(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a pipeline binary; run without -short")
	}
	bin := filepath.Join(t.TempDir(), "podnodefixture")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/podnodefixture").CombinedOutput(); err != nil {
		t.Fatalf("build the fixture: %v\n%s", err, out)
	}
	return bin
}

type launchedRun struct {
	st    *store.Store
	url   string
	runID string
	src   string
	bin   string
	// safety: the trigger's actor and environment, which the controller's run read derives the run context from.
	user       string
	triggerEnv map[string]string
}

func newLaunchedRun(t *testing.T, args map[string]string) *launchedRun {
	t.Helper()
	bin := podFixture(t)
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(controller.New(st, quiet).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	r := &launchedRun{st: st, url: srv.URL, runID: "run-pod-host", bin: bin}
	r.create(t, args)
	prevBinary := resolveLaunchedBinary
	t.Cleanup(func() { resolveLaunchedBinary = prevBinary })
	resolveLaunchedBinary = func(context.Context, string, *bincache.BinaryData, *slog.Logger) (remoteBinary, error) {
		return remoteBinary{entry: pipelineentry.Binary(bin)}, nil
	}
	r.src = t.TempDir()
	t.Setenv("SPARKWING_SOURCE_DIR", r.src)
	return r
}

// safety: a run of a GitHub repository dispatched to the controller, the only
// kind whose claims the controller lets memoize.
func (r *launchedRun) create(t *testing.T, args map[string]string) {
	t.Helper()
	ctx, now := store.WithoutCreditMetering(t.Context()), time.Now()
	if _, err := r.st.DB().ExecContext(ctx, `INSERT INTO repos (team, repo, dispatch, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT DO NOTHING`, string(store.DefaultTeam), store.RepoKey("acme", "widgets"), string(store.RepoDispatchController), now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	tn, err := r.st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.CreateTriggerWithRun(ctx, store.Trigger{
		ID: r.runID, Pipeline: "podnode", GithubOwner: "acme", GithubRepo: "widgets", Repo: "acme/widgets",
		GithubRepoID: 701, GitBranch: "main", GitSHA: strings.Repeat("a", 40), CreatedAt: now,
		TriggerUser: r.user, TriggerEnv: r.triggerEnv,
	}, store.Run{
		ID: r.runID, Pipeline: "podnode", Status: "pending", Args: args, GithubOwner: "acme", GithubRepo: "widgets",
		DeclaredRepo: "acme/widgets", CreatedAt: now, StartedAt: now,
		// safety: as the controller's trigger intake records the trigger's run flags on the run row.
		Invocation: runflags.FromTriggerEnv(r.triggerEnv).Invocation(),
	}); err != nil {
		t.Fatal(err)
	}
}

// safety: the same store, controller and checkout, so a memo entry one run records is another's to hit.
func (r *launchedRun) another(t *testing.T, runID string, args map[string]string) *launchedRun {
	t.Helper()
	next := &launchedRun{st: r.st, url: r.url, runID: runID, src: r.src}
	next.create(t, args)
	return next
}

func (r *launchedRun) claim(t *testing.T, nodeID string) string {
	t.Helper()
	now := time.Now()
	// safety: a retry waits out its backoff before the launcher may claim it, and these tests claim it at once.
	if n := r.node(t, nodeID); n.ReadyAt != nil && n.ReadyAt.After(now) {
		now = *n.ReadyAt
	}
	c, err := r.st.ClaimLaunch(t.Context(), store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"},
		store.LaunchClaimRequest{HolderID: "job-" + nodeID, Lease: time.Minute, Deadline: time.Hour, RunID: r.runID, NodeID: nodeID},
		now)
	if err != nil || c == nil {
		t.Fatalf("claim %s: %+v %v", nodeID, c, err)
	}
	return c.Token
}

func (r *launchedRun) host(t *testing.T, nodeID string) (token string, err error) {
	t.Helper()
	token = r.claim(t, nodeID)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	return token, runLaunchedNode(ctx, r.url, r.runID, nodeID, token, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func (r *launchedRun) objects(t *testing.T) storage.ArtifactStore {
	t.Helper()
	objs, err := fs.NewArtifactStore(r.st.OutputDir())
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

func (r *launchedRun) node(t *testing.T, nodeID string) *store.Node {
	t.Helper()
	n, err := r.st.GetNode(t.Context(), r.runID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The launcher Job plans the run in the pipeline's own process, the
// controller accepts that plan, and a node's Job hosts the node and reports
// the attempt; neither the claim token nor the runner's credentials reach the
// node, and the token never reaches its log.
func TestLaunchedNode_PlansHostsAndReports(t *testing.T) {
	t.Setenv("SPARKWING_AGENT_TOKEN", "swr_runner-credential")
	t.Setenv("SPARKWING_CACHE_GRANT", "swcg1.runner-grant")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "runner-aws")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	envOut, echo := filepath.Join(t.TempDir(), "env"), filepath.Join(t.TempDir(), "echo")
	r := newLaunchedRun(t, map[string]string{"envout": envOut, "echo": echo, "pass": "pod-secret-value-123"})
	if _, err := r.host(t, store.PlanNodeID); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if n := r.node(t, "build"); n.Status != "pending" {
		t.Fatalf("build after the plan = %s, want pending", n.Status)
	}
	// safety: the node prints its own claim token, as a step that echoes a stolen credential would.
	token := r.claim(t, "build")
	if err := os.WriteFile(echo, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := runLaunchedNode(ctx, r.url, r.runID, "build", token, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("host build: %v", err)
	}
	if n := r.node(t, "build"); n.Status != "done" || n.Outcome != string(sparkwing.Success) {
		t.Fatalf("build = %s/%s (%s), want done/success", n.Status, n.Outcome, n.Error)
	}
	env, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{token, "swr_runner-credential", "swcg1.runner-grant", "runner-aws", "SPARKWING_CONTROLLER_URL"} {
		if strings.Contains(string(env), leaked) {
			t.Errorf("the node's environment holds %q:\n%s", leaked, env)
		}
	}
	if !strings.Contains(string(env), "SPARKWING_NODE_ID=build") {
		t.Errorf("the node's environment lacks its identity:\n%s", env)
	}
	l := segmentController{st: r.st}.nodeLog(t, r.runID, "build")
	logs, err := l.Read(t.Context(), r.objects(t), storage.ReadOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if l.Manifest == nil || l.Manifest.Status != logseg.StatusComplete {
		t.Errorf("the node's log manifest = %+v, want it sealed complete", l.Manifest)
	}
	if strings.Contains(string(logs), token) || !strings.Contains(string(logs), "echo ***") {
		t.Errorf("the node's log = %s, want its token masked", logs)
	}
	if strings.Contains(string(logs), "pod-secret-value-123") || !strings.Contains(string(logs), "pass ***") {
		t.Errorf("the node's log = %s, want its secret input masked", logs)
	}
}

// A launched run plans with its trigger's actor and pull request, which the
// launcher's claims cannot read from the trigger, and each node's own plan
// sees the same ones, so the node runs rather than failing for plan drift.
func TestLaunchedNode_PlansWithTheTriggersActorAndPullRequest(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	base := newLaunchedRun(t, nil)
	r := &launchedRun{
		st: base.st, url: base.url, runID: "run-pull-request", src: base.src, user: "octocat",
		triggerEnv: map[string]string{sparkwing.EnvGitHubEventName: sparkwing.EventPullRequest, sparkwing.EnvPRNumber: "42"},
	}
	envOut := filepath.Join(t.TempDir(), "env")
	r.create(t, map[string]string{"envout": envOut})
	if _, err := r.host(t, store.PlanNodeID); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := r.host(t, "build"); err != nil {
		t.Fatalf("host build: %v", err)
	}
	env, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "ACTOR=octocat#42") {
		t.Fatalf("the node's environment lacks the trigger's actor and pull request:\n%s", env)
	}
}

// A node whose own plan hashes otherwise than the accepted plan fails with
// plan_drift before its body runs, and the attempt is reported failed.
func TestLaunchedNode_PlanDriftFailsTheNode(t *testing.T) {
	envOut := filepath.Join(t.TempDir(), "env")
	r := newLaunchedRun(t, map[string]string{"envout": envOut, "drift": filepath.Join(t.TempDir(), "plans")})
	if _, err := r.host(t, store.PlanNodeID); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := r.host(t, "build"); err == nil {
		t.Fatal("a drifted node reported success")
	}
	n := r.node(t, "build")
	if n.Status != "done" || n.Outcome != string(sparkwing.Failed) || n.FailureReason != "plan_drift" ||
		!strings.Contains(n.Error, "plans differently") {
		t.Fatalf("build = %s/%s (%s), want failed for plan drift", n.Status, n.Outcome, n.Error)
	}
	if _, err := os.Stat(envOut); !os.IsNotExist(err) {
		t.Fatalf("the drifted node's body ran: %v", err)
	}
}

// A dispatched pod that was handed no accepted spec_hash fails its node with
// plan_drift rather than trusting its own plan.
func TestPodNode_WithoutAnAcceptedHashFailsClosed(t *testing.T) {
	envOut := filepath.Join(t.TempDir(), "env")
	r := newLaunchedRun(t, map[string]string{"envout": envOut})
	if _, err := r.host(t, store.PlanNodeID); err != nil {
		t.Fatalf("plan: %v", err)
	}
	token := r.claim(t, "build")
	state := &claimState{Client: client.NewWithToken(r.url, nil, token), runID: r.runID, nodeID: "build"}
	res, err := hostPodNode(t.Context(), podNode{
		entry: pipelineentry.Binary(podFixture(t)), dir: t.TempDir(), controllerURL: r.url, token: token, runID: r.runID, nodeID: "build",
		state: state, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	report := state.attempt(runner.Result{Outcome: res.Outcome, Err: res.Err}, nil)
	if res.Outcome != sparkwing.Failed || report.FailureReason != "plan_drift" || !strings.Contains(report.Error, "no accepted spec_hash") {
		t.Fatalf("result %+v, report %+v; want a plan_drift failure", res, report)
	}
	if _, err := os.Stat(envOut); !os.IsNotExist(err) {
		t.Fatalf("the node's body ran: %v", err)
	}
}

// A retry_auto node's every attempt is its own claim and pod: before_run runs
// in each, as every dispatch did, and after_run in the last, the one the
// controller does not retry.
func TestLaunchedNode_RetryAutoRunsAfterRunLast(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace")
	r := newLaunchedRun(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "trace": trace, "auto": "true"})
	requests := r.recordRequests(t)
	if _, err := r.host(t, store.PlanNodeID); err != nil {
		t.Fatalf("plan: %v", err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if _, err := r.host(t, "build"); err == nil {
			t.Fatalf("attempt %d of a failing node succeeded", attempt)
		}
		want := "pending"
		if attempt == 3 {
			want = "done"
		}
		if n := r.node(t, "build"); n.Status != want || n.AttemptsConsumed != attempt {
			t.Fatalf("after attempt %d build = %s with %d attempts (%q), want %s", attempt, n.Status, n.AttemptsConsumed, n.Error, want)
		}
	}
	wantTrace(t, trace, "before\nbefore\nbefore\nafter:step \"run\": still broken\n")
	sent, err := os.ReadFile(requests)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if !strings.Contains(string(sent), fmt.Sprintf(`"op":"run_step","node":"build","step":"run","attempt":%d`, attempt)) {
			t.Errorf("no run_step for attempt %d in the requests:\n%s", attempt, sent)
		}
	}
}

// hack: the Go SDK ignores the attempt ordinal, so a wrapper copies every request to the returned file instead.
func (r *launchedRun) recordRequests(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log, wrapper := filepath.Join(dir, "requests"), filepath.Join(dir, "pipeline")
	script := fmt.Sprintf("#!/bin/sh\ntee -a '%s' <&3 3<&- 4>&- | '%s' \"$@\" 3<&0 0</dev/null\n", log, r.bin)
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	resolveLaunchedBinary = func(context.Context, string, *bincache.BinaryData, *slog.Logger) (remoteBinary, error) {
		return remoteBinary{entry: pipelineentry.Binary(wrapper)}, nil
	}
	return log
}

// A retry_auto node whose before_run refuses never runs its body, though the
// controller dispatches it again.
func TestLaunchedNode_RetryAutoNeverRunsABodyItsBeforeRunRefused(t *testing.T) {
	envOut := filepath.Join(t.TempDir(), "env")
	r := newLaunchedRun(t, map[string]string{
		"envout": envOut, "trace": filepath.Join(t.TempDir(), "trace"), "auto": "true", "refuse": "true",
	})
	if _, err := r.host(t, store.PlanNodeID); err != nil {
		t.Fatalf("plan: %v", err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if _, err := r.host(t, "build"); err == nil {
			t.Fatalf("attempt %d of a refused node succeeded", attempt)
		}
		if _, err := os.Stat(envOut); !os.IsNotExist(err) {
			t.Fatalf("attempt %d ran the body of a refused node: %v", attempt, err)
		}
	}
	if n := r.node(t, "build"); n.Status != "done" || n.Outcome != string(sparkwing.Failed) {
		t.Fatalf("build = %s/%s, want done/failed", n.Status, n.Outcome)
	}
}

// A plain retry re-runs in the one pod its claim started, with before_run in
// the first attempt and after_run in the last.
func TestLaunchedNode_PlainRetryRunsInThePod(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace")
	r := newLaunchedRun(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "trace": trace})
	if _, err := r.host(t, store.PlanNodeID); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := r.host(t, "build"); err == nil {
		t.Fatal("a failing node succeeded")
	}
	if n := r.node(t, "build"); n.Status != "done" || n.Outcome != string(sparkwing.Failed) || n.AttemptsConsumed != 1 {
		t.Fatalf("build = %s/%s with %d claims, want done/failed with 1", n.Status, n.Outcome, n.AttemptsConsumed)
	}
	wantTrace(t, trace, "before\nafter:step \"run\": still broken\n")
}

func wantTrace(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("hook calls = %q, want %q", data, want)
	}
}

// A node in a concurrency group takes and releases its slot through its claim.
func TestLaunchedNode_RunsItsConcurrencyGroupThroughTheClaim(t *testing.T) {
	r := newLaunchedRun(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "group": "true"})
	if _, err := r.host(t, store.PlanNodeID); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := r.host(t, "build"); err != nil {
		t.Fatalf("host build: %v", err)
	}
	if n := r.node(t, "build"); n.Status != "done" || n.Outcome != string(sparkwing.Success) {
		t.Fatalf("build = %s/%s (%s), want done/success", n.Status, n.Outcome, n.Error)
	}
}

// The placement labels in the pipeline's YAML survive the planning claim, as
// plan code never names them.
func TestLaunchedNode_PlanKeepsThePipelinesRequires(t *testing.T) {
	r := newLaunchedRun(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env")})
	yaml := "pipelines:\n  - name: podnode\n    entrypoint: Podnode\n    requires: [trusted]\n"
	if err := os.MkdirAll(filepath.Join(r.src, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.src, ".sparkwing", "sparkwing.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.host(t, store.PlanNodeID); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := r.node(t, "build").NeedsLabels; !slices.Contains(got, "trusted") {
		t.Fatalf("build needs %v, want trusted from the pipeline's YAML", got)
	}
}

// A log that lost lines seals as incomplete, so readers never take the prefix
// for the whole log.
func TestPodNodeLog_SealsIncompleteAfterALoss(t *testing.T) {
	ctrl, tok := newSegmentController(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	nlog := &podNodeLog{sink: openNodeLogSink(t.Context(), client.NewWithToken(ctrl.url, nil, tok), "run-s", "a", logger), logger: logger}
	if nlog.sink == nil {
		t.Fatal("the controller issued no segment URLs")
	}
	nlog.Emit(sparkwing.LogRecord{Msg: "kept"})
	nlog.RecordLoss(2)
	nlog.seal(t.Context(), false)
	if m := ctrl.log(t).Manifest; m == nil || m.Status != logseg.StatusIncomplete || m.DroppedLines != 2 {
		t.Fatalf("manifest = %+v, want incomplete with 2 dropped lines", m)
	}
}

// A memoized node's second run copies the first's result through its claim
// instead of running the body again, and one that publishes artifacts takes
// the first run's artifact manifest with it; a source that recorded no
// manifest runs as a miss, since a hit would hand its consumers nothing.
func TestLaunchedNode_MemoHitsThroughTheClaim(t *testing.T) {
	t.Run("Hit", func(t *testing.T) { podMemoRuns(t, false, true, sparkwing.Cached, "body\n") })
	t.Run("ArtifactsHit", func(t *testing.T) { podMemoRuns(t, true, true, sparkwing.Cached, "body\n") })
	t.Run("NoManifestRunsAsAMiss", func(t *testing.T) { podMemoRuns(t, true, false, sparkwing.Success, "body\nbody\n") })
}

func podMemoRuns(t *testing.T, publish, objects bool, want sparkwing.Outcome, bodies string) {
	t.Helper()
	if objects {
		t.Setenv(ArtifactStoreEnvVar, "fs://"+t.TempDir())
	} else {
		t.Setenv(ArtifactStoreEnvVar, "")
	}
	marker := filepath.Join(t.TempDir(), "memo")
	args := map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "memo": marker}
	if publish {
		args["publish"] = "true"
	}
	first := newLaunchedRun(t, args)
	second := first.another(t, "run-pod-host-2", args)
	for i, r := range []*launchedRun{first, second} {
		if _, err := r.host(t, store.PlanNodeID); err != nil {
			t.Fatalf("plan run %d: %v", i, err)
		}
		if _, err := r.host(t, "memoized"); err != nil {
			t.Fatalf("host memoized in run %d: %v", i, err)
		}
	}
	got := second.node(t, "memoized")
	if got.Status != "done" || got.Outcome != string(want) {
		t.Fatalf("second run's memoized = %s/%s (%s), want done/%s", got.Status, got.Outcome, got.Error, want)
	}
	wantTrace(t, marker, bodies)
	if src := first.node(t, "memoized"); publish && objects && (src.ArtifactManifest == "" || got.ArtifactManifest != src.ArtifactManifest) {
		t.Fatalf("second run's manifest = %q, want the first's %q", got.ArtifactManifest, src.ArtifactManifest)
	}
}

// safety: a controller-dispatched run reads `sparkwing pipeline trigger`'s run flags from its run row: the
// planning claim cuts the plan to --sw-only and each node's Job skips the steps outside the range or dry run.
func TestLaunchedNode_HonorsTheTriggersRunFlags(t *testing.T) {
	cases := []struct {
		name  string
		env   map[string]string
		nodes []string
		ran   []string
	}{
		{
			name: "only", env: map[string]string{runflags.EnvOnly: "ship"}, nodes: []string{"prep", "ship"},
			ran: []string{"compile", "package", "prep", "upload"},
		},
		{
			name: "step range", env: map[string]string{runflags.EnvStartAt: "package", runflags.EnvStopAt: "package"},
			nodes: []string{"build", "lint", "prep", "ship"}, ran: []string{"lint", "package", "prep"},
		},
		{name: "dry run", env: map[string]string{runflags.EnvDryRun: "1"}, nodes: []string{"build", "lint", "prep", "ship"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GORACE", "atexit_sleep_ms=0")
			base := newLaunchedRun(t, nil)
			r := &launchedRun{st: base.st, url: base.url, runID: "run-flags", src: base.src, triggerEnv: c.env}
			ranged := filepath.Join(t.TempDir(), "ranged")
			r.create(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "ranged": ranged})
			if _, err := r.host(t, store.PlanNodeID); err != nil {
				t.Fatalf("plan: %v", err)
			}
			var planned []string
			for _, id := range []string{"build", "lint", "prep", "ship"} {
				if _, err := r.st.GetNode(t.Context(), r.runID, id); err == nil {
					planned = append(planned, id)
				}
			}
			if !slices.Equal(planned, c.nodes) {
				t.Fatalf("planned nodes = %v, want %v", planned, c.nodes)
			}
			for _, id := range planned {
				if _, err := r.host(t, id); err != nil {
					t.Fatalf("host %s: %v", id, err)
				}
			}
			data, err := os.ReadFile(ranged)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			ran := strings.Fields(string(data))
			slices.Sort(ran)
			if !slices.Equal(ran, c.ran) {
				t.Fatalf("steps that ran = %v, want %v", ran, c.ran)
			}
		})
	}
}

// safety: --sw-no-cache reaches the memo of a node a claim hosts, so the node runs its body over a stored result.
func TestLaunchedNode_NoCacheMissesTheMemo(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv(ArtifactStoreEnvVar, "")
	marker := filepath.Join(t.TempDir(), "memo")
	args := map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "memo": marker}
	first := newLaunchedRun(t, args)
	second := &launchedRun{
		st: first.st, url: first.url, runID: "run-no-cache", src: first.src,
		triggerEnv: map[string]string{runflags.EnvNoCache: "1"},
	}
	second.create(t, args)
	for i, r := range []*launchedRun{first, second} {
		if _, err := r.host(t, store.PlanNodeID); err != nil {
			t.Fatalf("plan run %d: %v", i, err)
		}
		if _, err := r.host(t, "memoized"); err != nil {
			t.Fatalf("host memoized in run %d: %v", i, err)
		}
	}
	if got := second.node(t, "memoized"); got.Outcome != string(sparkwing.Success) {
		t.Fatalf("no-cache run's memoized = %s/%s (%s), want a fresh success", got.Status, got.Outcome, got.Error)
	}
	wantTrace(t, marker, "body\nbody\n")
}

// A secret that straddles the session's split of a long plan line is masked
// whole, never printed as two fragments.
func TestPodPlanSink_MasksASecretAcrossASplitLine(t *testing.T) {
	const secret = "hunter2-straddles-the-split"
	masker := secrets.NewMasker()
	masker.Register(secret)
	var out strings.Builder
	sink, err := newPodPlanSink(t.Context(), "run-split", masker, &out)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("x", 1<<20-10) + secret[:10]
	sink.Text(nodehost.LevelInfo, head)
	sink.Text(nodehost.LevelInfo, secret[10:]+" tail")
	sink.end()
	if got := strings.ReplaceAll(out.String(), "\n", ""); strings.Contains(got, secret[:10]) || !strings.Contains(got, "*** tail") {
		t.Fatalf("plan output ends %q, want the split secret masked whole", got[max(0, len(got)-80):])
	}
}

// A plan whose stderr blocks past the session's grace ends with the head of a
// split secret still held and its rest still queued; ending the sink then
// prints neither piece.
func TestPodPlanSink_EndingDropsAHeldSecretAndItsLateRest(t *testing.T) {
	const secret = "hunter2-straddles-the-split"
	masker := secrets.NewMasker()
	masker.Register(secret)
	out := &blockingStderr{entered: make(chan struct{}), release: make(chan struct{})}
	sink, err := newPodPlanSink(t.Context(), "run-blocked", masker, out)
	if err != nil {
		t.Fatal(err)
	}
	delivered := make(chan struct{})
	go func() {
		defer close(delivered)
		sink.Text(nodehost.LevelInfo, strings.Repeat("x", 1<<20-10)+secret[:10])
		sink.Text(nodehost.LevelInfo, secret[10:]+" tail")
	}()
	<-out.entered
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		sink.end()
	}()
	close(out.release)
	<-ended
	<-delivered
	got := out.String()
	if strings.Contains(got, secret[:10]) || strings.Contains(got, secret[10:]) {
		t.Fatalf("plan output after the end holds a piece of the secret: ...%q", got[max(0, len(got)-80):])
	}
}

// hack: holds its first write until release, standing in for a stalled container log.
type blockingStderr struct {
	mu      sync.Mutex
	buf     strings.Builder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingStderr) Write(p []byte) (int, error) {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *blockingStderr) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
