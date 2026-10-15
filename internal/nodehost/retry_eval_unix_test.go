//go:build unix

package nodehost

import (
	"context"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineenv"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type sourceTargetsGone struct{ *store.Store }

func (s sourceTargetsGone) GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error) {
	if runID == "retry-source" && nodeID == "targets" {
		return nil, store.ErrNotFound
	}
	return s.Store.GetNodeOutput(ctx, runID, nodeID)
}

type retryFixture struct {
	t        *testing.T
	bin, dir string
	st       *store.Store
	mu       sync.Mutex
	evalWall map[string]time.Duration
}

func newRetryFixture(t *testing.T) *retryFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs an SDK pipeline")
	}
	bin := filepath.Join(t.TempDir(), "retryfixture")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/retryfixture").CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	f := &retryFixture{t: t, bin: bin, dir: t.TempDir(), st: openStore(t), evalWall: map[string]time.Duration{}}
	f.mode("stable")
	return f
}

func (f *retryFixture) mode(m string) {
	if err := os.WriteFile(filepath.Join(f.dir, "mode"), []byte(m), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *retryFixture) run(ctx context.Context, id string, r *Rehydration, state storage.StateStoreOld) RunResult {
	f.t.Helper()
	srv, err := Start(nil)
	if err != nil {
		f.t.Fatal(err)
	}
	//nolint:contextcheck // Close bounds its own shutdown.
	defer func() { _ = srv.Close() }()
	logs := &memLogs{records: map[string][]sparkwing.LogRecord{}}
	cfg := Config{
		Session:  SessionConfig{Entry: pipelineentry.Binary(f.bin), Dir: f.dir, Grace: time.Second, Env: append(engineenv.Strip(os.Environ()), "GORACE=atexit_sleep_ms=0"), Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}},
		Pipeline: "probe", Args: map[string]string{"mode-file": filepath.Join(f.dir, "mode"), "trace-dir": f.dir},
		State: state, Logger: slog.New(slog.DiscardHandler), Heartbeat: time.Hour, Rehydrate: r,
		Attempts: &Attempts{
			Routes: srv, Resolver: fixtureResolver{}, Masker: secrets.NewMasker(), Claim: outputController(f.t, f.st),
			OpenLog: func(_ context.Context, runID, node string) (NodeLog, error) {
				return memLog{logs: logs, node: runID + "/" + node}, nil
			},
			Usage: func(_ context.Context, n *NodeRun, u Usage) {
				if n.memoOutput != nil {
					f.mu.Lock()
					f.evalWall[n.Plan.ID] = u.Wall
					f.mu.Unlock()
				}
			},
		},
	}
	cfg.Run.RunID = id
	res, err := Execute(ctx, cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func (f *retryFixture) bodies(id string) int {
	b, _ := os.ReadFile(filepath.Join(f.dir, id))
	return strings.Count(string(b), "body")
}

func (f *retryFixture) source() {
	f.t.Helper()
	if res := f.run(f.t.Context(), "retry-source", nil, f.st); res.Status != "failed" || res.Nodes["test-good"].Outcome != sparkwing.Success {
		f.t.Fatalf("source run %s nodes %+v, want failed with test-good succeeded", res.Status, res.Nodes)
	}
}

func TestARetryOfAGeneratedFailureRunsOnlyIt(t *testing.T) {
	f := newRetryFixture(t)
	f.source()
	res := f.run(t.Context(), "retry", &Rehydration{SourceRunID: "retry-source"}, f.st)
	if res.Status != "success" {
		t.Fatalf("retry %s: %v", res.Status, res.Err)
	}
	if got := map[string]int{"targets": f.bodies("targets"), "good": f.bodies("good"), "bad": f.bodies("bad"), "summary": f.bodies("summary")}; got["targets"] != 1 || got["good"] != 1 || got["bad"] != 2 || got["summary"] != 1 {
		t.Fatalf("bodies %v: the retry reruns only the failed check and the summary waiting on it", got)
	}
}

func TestARetryWhoseGeneratorOutputIsGoneRunsItAndItsNodes(t *testing.T) {
	f := newRetryFixture(t)
	f.source()
	res := f.run(t.Context(), "retry", &Rehydration{SourceRunID: "retry-source"}, sourceTargetsGone{f.st})
	if res.Status != "success" || f.bodies("targets") != 2 || f.bodies("good") != 2 {
		t.Fatalf("retry %s targets=%d good=%d: a generator whose output is gone runs again with every node it adds",
			res.Status, f.bodies("targets"), f.bodies("good"))
	}
}

// safety: a source run that never fully recorded its generator's nodes, as a host stopped mid-add leaves it.
type sourceBatchUnrecorded struct{ *store.Store }

func (s sourceBatchUnrecorded) ListNodes(ctx context.Context, runID string) ([]*store.Node, error) {
	rows, err := s.Store.ListNodes(ctx, runID)
	if runID == "retry-source" {
		for _, row := range rows {
			row.Generated = nil
		}
	}
	return rows, err
}

func TestACopiedGeneratorsEvalKeepsTheNodesTimeout(t *testing.T) {
	f := newRetryFixture(t)
	f.source()
	f.mode("hang")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	res := f.run(ctx, "retry", &Rehydration{SourceRunID: "retry-source"}, sourceBatchUnrecorded{f.st})
	if ctx.Err() != nil {
		t.Fatal("the retry only ended with its context: a hung generator eval held the scheduler")
	}
	if wall := f.evalWall["targets"]; wall > 5*time.Second {
		t.Fatalf("the copied generator's eval ran %s past its node's 400ms timeout", wall)
	}
	for _, e := range res.Nodes["targets"].expansions {
		if e.err == nil || !strings.Contains(e.err.Error(), "timeout exceeded (400ms)") {
			t.Errorf("the generator's error is %v, want the node's timeout named", e.err)
		}
	}
}

// safety: the source run's recorded nodes are the retry's, so a good check whose generator now builds it
// differently is still copied, and a check that runs again under a changed generator fails with plan_drift.
func TestARetryKeepsTheGeneratedNodesItsSourceRecorded(t *testing.T) {
	f := newRetryFixture(t)
	f.source()
	f.mode("changed")
	res := f.run(t.Context(), "retry", &Rehydration{SourceRunID: "retry-source"}, f.st)
	if f.bodies("good") != 1 || f.evalWall["targets"] != 0 {
		t.Fatalf("good=%d, targets eval %s: the copied generator's recorded nodes are added without generating them",
			f.bodies("good"), f.evalWall["targets"])
	}
	if bad := res.Nodes["test-bad"]; res.Status != "failed" || bad.Err == nil || !strings.Contains(bad.Err.Error(), "differently") {
		t.Fatalf("retry %s test-bad %v, want test-bad failed for plan drift", res.Status, bad.Err)
	}
}
