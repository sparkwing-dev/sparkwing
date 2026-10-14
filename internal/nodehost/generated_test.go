//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func stampedNode(t *testing.T, raw string) nodeproto.PlanNode {
	t.Helper()
	hash, err := nodeproto.SpecHash([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	var n nodeproto.PlanNode
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	n.SpecHash = hash
	return n
}

// hack: the rows are recorded through the scheduler's own expansion path, as a run host records them, without a run.
func genPod(t *testing.T, mode string) (Config, *store.Store, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a protocol fixture")
	}
	bin := filepath.Join(t.TempDir(), "genfixture")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/genfixture").CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	st := openStore(t)
	routes, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = routes.Close() })
	trace := filepath.Join(t.TempDir(), "evals")
	logs := &memLogs{records: map[string][]sparkwing.LogRecord{}}
	cfg := Config{
		Session:  SessionConfig{Entry: pipelineentry.Binary(bin), Env: []string{"GENFIXTURE_MODE=" + mode, "GENFIXTURE_TRACE=" + trace}, Grace: 20 * time.Millisecond},
		Pipeline: "probe", Run: nodeproto.RunContext{RunID: "run-probe"}, State: st, Logger: slog.New(slog.DiscardHandler),
		Attempts: &Attempts{
			Routes: routes, Local: st, Resolver: fixtureResolver{}, Masker: secrets.NewMasker(),
			OpenLog: func(_ context.Context, _, node string) (NodeLog, error) { return memLog{logs: logs, node: node}, nil },
		},
	}
	if err := st.CreateRun(t.Context(), store.Run{ID: "run-probe", Pipeline: "probe", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	source := stampedNode(t, `{"id":"targets","deps":[],"closures":{"generator":["targets/generator/0"]},"modifiers":{"timeout_ms":20}}`)
	run := &Run{ID: "run-probe", Doc: nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{source}}, State: st, Logger: cfg.Logger}
	s := &scheduler{run: run, results: map[string]Result{}, started: map[string]bool{}}
	if err := s.add(t.Context(), t.Context(), []nodeproto.PlanNode{source}, nil); err != nil {
		t.Fatal(err)
	}
	child := `{"id":"child","deps":["targets"],"modifiers":{"timeout_ms":20}}`
	if mode == "nested" {
		child = `{"id":"child","deps":["targets"],"modifiers":{"timeout_ms":20},"closures":{"generator":["child/generator/0"]}}`
	}
	children := []nodeproto.PlanNode{stampedNode(t, child), stampedNode(t, `{"id":"recover","deps":[],"on_failure_of":"child"}`)}
	if err := s.addGenerated(t.Context(), "targets", expansion{closure: "targets/generator/0", nodes: children}); err != nil {
		t.Fatal(err)
	}
	if mode == "nested" {
		grandchild := []nodeproto.PlanNode{stampedNode(t, `{"id":"grandchild","deps":["child"]}`)}
		if err := s.addGenerated(t.Context(), "child", expansion{closure: "child/generator/0", nodes: grandchild}); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, st, trace
}

func hostGenerated(t *testing.T, ctx context.Context, cfg Config, st *store.Store, id string) Result {
	t.Helper()
	row, err := st.GetNode(ctx, "run-probe", id)
	if err != nil {
		t.Fatal(err)
	}
	return HostDispatchedNode(ctx, cfg, id, row.SpecHash)
}

func evals(t *testing.T, trace string) []string {
	t.Helper()
	data, err := os.ReadFile(trace)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

// A generated recovery node needs no source of its own: its host runs the
// spec its row records, loading its code with the generator that returned it.
func TestAPodRunsAGeneratedRecoveryNode(t *testing.T) {
	cfg, st, _ := genPod(t, "normal")
	if err := st.FinishNodeWithReason(t.Context(), "run-probe", "child", "failed", "body failed", nil, "", nil); err != nil {
		t.Fatal(err)
	}
	if res := hostGenerated(t, t.Context(), cfg, st, "recover"); res.Outcome != sparkwing.Success {
		t.Fatalf("generated recovery = %s (%s): %v", res.Outcome, res.Reason, res.Err)
	}
}

// A node a generated node generated is loaded by running each generator of
// the chain in turn, its source's first.
func TestAPodRunsANestedGeneratedNode(t *testing.T) {
	cfg, st, trace := genPod(t, "nested")
	if res := hostGenerated(t, t.Context(), cfg, st, "grandchild"); res.Outcome != sparkwing.Success {
		t.Fatalf("nested generated node = %s (%s): %v", res.Outcome, res.Reason, res.Err)
	}
	if got := strings.Join(evals(t, trace), ","); got != "targets/generator/0,child/generator/0" {
		t.Fatalf("evals = %s, want the chain from the plan's source", got)
	}
}

// perf: the pod reads the node's spec from its row, so the only generator run
// is the one that loads the node's code in its attempt.
func TestAGeneratedNodesPodRunsItsGeneratorOnce(t *testing.T) {
	cfg, st, trace := genPod(t, "normal")
	if res := hostGenerated(t, t.Context(), cfg, st, "child"); res.Outcome != sparkwing.Success {
		t.Fatalf("generated node = %s: %v", res.Outcome, res.Err)
	}
	if got := evals(t, trace); len(got) != 1 {
		t.Fatalf("generator runs in one pod = %v, want 1", got)
	}
}

// safety: the generator that loads a generated node's code runs inside its
// attempt, so the node's own timeout bounds a generator that hangs.
func TestAGeneratedNodesTimeoutBoundsItsGenerator(t *testing.T) {
	cfg, st, _ := genPod(t, "hang")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if res := hostGenerated(t, ctx, cfg, st, "child"); res.Reason != store.FailureTimeout || ctx.Err() != nil {
		t.Fatalf("hung generator = %s (%s): %v, want the child's 20ms timeout", res.Outcome, res.Reason, res.Err)
	}
}

func resumedFanOut(t *testing.T, sourceOutcome, childOutcome string) (*scheduler, *store.Store, *int) {
	t.Helper()
	st := openStore(t)
	ctx := t.Context()
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	doc := fanOutDoc()
	first := &scheduler{
		run:     &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler)},
		results: map[string]Result{}, started: map[string]bool{},
	}
	if err := first.add(ctx, ctx, doc.Nodes, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNodeWithReason(ctx, testRunID, "targets", sourceOutcome, "", []byte(`"from-targets"`), "", nil); err != nil {
		t.Fatal(err)
	}
	if childOutcome != "" {
		child := nodeproto.PlanNode{ID: "test-a", Deps: deps("targets"), SpecHash: "sha256:a"}
		if err := first.addGenerated(ctx, "targets", expansion{closure: testGenerator, nodes: []nodeproto.PlanNode{child}}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishNodeWithReason(ctx, testRunID, "test-a", childOutcome, "test failure", nil, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{Dispatch: remoteHost(st, sparkwing.Success, "", nil)}
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest)}
	generated := 0
	s := &scheduler{
		run: run, results: map[string]Result{}, started: map[string]bool{}, exec: chain(&cfg), resume: true,
		regenerate: func(_ context.Context, n *NodeRun, _ json.RawMessage) []expansion {
			generated++
			return []expansion{{closure: testGenerator, nodes: []nodeproto.PlanNode{{ID: "test-a", Deps: deps(n.Plan.ID)}}}}
		},
	}
	if err := s.add(ctx, ctx, doc.Nodes, nil); err != nil {
		t.Fatal(err)
	}
	return s, st, &generated
}

// safety: a resumed run's generated nodes are the rows it recorded, so a
// failed one keeps failing the run, whatever its generator would return now.
func TestAResumedRunKeepsAFailedGeneratedNode(t *testing.T) {
	s, _, generated := resumedFanOut(t, "success", "failed")
	s.loop(t.Context())
	status, err := s.verdict(t.Context())
	if status != "failed" || *generated != 0 || s.results["summary"].Outcome != sparkwing.Cancelled {
		t.Fatalf("resumed run = %s (%v), generator runs %d, summary %s; want failed with test-a's failure kept",
			status, err, *generated, s.results["summary"].Outcome)
	}
}

// A resumed run whose generator source a memo hit copied, and whose batch was
// never recorded, generates it, as for a source that ran.
func TestAResumedRunGeneratesACachedSourcesUnrecordedNodes(t *testing.T) {
	s, _, generated := resumedFanOut(t, "cached", "")
	s.loop(t.Context())
	if status, err := s.verdict(t.Context()); status != "success" || *generated != 1 {
		t.Fatalf("resumed run = %s (%v), generator runs %d; want success with the cached source's nodes generated once",
			status, err, *generated)
	}
}
