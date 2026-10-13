//go:build unix

package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var (
	podFixtureOnce sync.Once
	podFixturePath string
	podFixtureErr  error
)

func podFixture(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a pipeline binary; run without -short")
	}
	podFixtureOnce.Do(func() {
		dir, err := os.MkdirTemp("", "podnodefixture")
		if err != nil {
			podFixtureErr = err
			return
		}
		podFixturePath = filepath.Join(dir, "podnodefixture")
		out, err := exec.Command("go", "build", "-o", podFixturePath, "./testdata/podnodefixture").CombinedOutput()
		if err != nil {
			podFixtureErr = &buildError{err: err, out: string(out)}
		}
	})
	if podFixtureErr != nil {
		t.Fatalf("build the fixture: %v", podFixtureErr)
	}
	return podFixturePath
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.out }

type launchedRun struct {
	st    *store.Store
	url   string
	runID string
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
	ctx := t.Context()
	r := &launchedRun{st: st, url: srv.URL, runID: "run-pod-host"}
	if err := st.CreateRun(ctx, store.Run{ID: r.runID, Pipeline: "podnode", Status: "pending", Args: args, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePlanNode(ctx, store.DefaultTeam, r.runID, time.Now()); err != nil {
		t.Fatal(err)
	}
	prevBinary := resolveLaunchedBinary
	t.Cleanup(func() { resolveLaunchedBinary = prevBinary })
	resolveLaunchedBinary = func(context.Context, string, string, string, string, string, string, *slog.Logger) (remoteBinary, error) {
		return remoteBinary{path: bin}, nil
	}
	t.Setenv("SPARKWING_SOURCE_DIR", t.TempDir())
	return r
}

func (r *launchedRun) claim(t *testing.T, nodeID string) string {
	t.Helper()
	c, err := r.st.ClaimLaunch(t.Context(), store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"},
		store.LaunchClaimRequest{HolderID: "job-" + nodeID, Lease: time.Minute, Deadline: time.Hour, RunID: r.runID, NodeID: nodeID},
		time.Now())
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
	r := newLaunchedRun(t, map[string]string{"envout": envOut, "echo": echo})
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
		binary: podFixture(t), dir: t.TempDir(), controllerURL: r.url, token: token, runID: r.runID, nodeID: "build",
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
