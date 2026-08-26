package orchestrator_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// registerOnce avoids "duplicate Register" panics when a test file's
// pipelines are registered at package init.
var registerOnce sync.Map

func register(name string, factory func() sparkwing.Pipeline[sparkwing.NoInputs]) {
	if _, loaded := registerOnce.LoadOrStore(name, struct{}{}); loaded {
		return
	}
	sparkwing.Register[sparkwing.NoInputs](name, factory)
}

type okPipe struct{ sparkwing.Base }

func (okPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, func(ctx context.Context) error {
		sparkwing.Info(ctx, "work complete")
		return nil
	})
	return nil
}

type failPipe struct{ sparkwing.Base }

func (failPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, func(ctx context.Context) error { return errors.New("boom") })
	return nil
}

type fanOutOK struct{ sparkwing.Base }

func (fanOutOK) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	setup := sparkwing.Job(plan, "setup", func(ctx context.Context) error { return nil })
	a := sparkwing.Job(plan, "a", func(ctx context.Context) error { return nil }).Needs(setup)
	b := sparkwing.Job(plan, "b", func(ctx context.Context) error { return nil }).Needs(setup)
	sparkwing.Job(plan, "fin", func(ctx context.Context) error { return nil }).Needs(a, b)
	return nil
}

type middleFails struct{ sparkwing.Base }

func (middleFails) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	a := sparkwing.Job(plan, "a", func(ctx context.Context) error { return nil })
	b := sparkwing.Job(plan, "b", func(ctx context.Context) error { return errors.New("mid fail") }).Needs(a)
	sparkwing.Job(plan, "c", func(ctx context.Context) error { return nil }).Needs(b)
	return nil
}

// inflightCancelStarted is closed by the orch-cancel-inflight victim
// job the moment its body begins, so the test can cancel the run ctx
// while the node is genuinely in-flight.
var inflightCancelStarted chan struct{}

type cancelInflight struct{ sparkwing.Base }

func (cancelInflight) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "victim", func(ctx context.Context) error {
		close(inflightCancelStarted)
		<-ctx.Done()
		return ctx.Err()
	})
	return nil
}

type refBuildOut struct {
	Tag string `json:"tag"`
}
type refBuild struct {
	sparkwing.Base
	sparkwing.Produces[refBuildOut]
}

func (j *refBuild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (refBuild) run(ctx context.Context) (refBuildOut, error) {
	return refBuildOut{Tag: "v9"}, nil
}

type refDeploy struct {
	sparkwing.Base
	Build sparkwing.Ref[refBuildOut]
}

func (d *refDeploy) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", d.run)
	return nil, nil
}

func (d *refDeploy) run(ctx context.Context) error {
	got := d.Build.Get(ctx)
	if got.Tag != "v9" {
		return fmt.Errorf("ref got %q, want v9", got.Tag)
	}
	return nil
}

type refPipe struct{ sparkwing.Base }

func (refPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", &refBuild{})
	sparkwing.Job(plan, "deploy", &refDeploy{Build: sparkwing.RefTo[refBuildOut](build)}).Needs(build)
	return nil
}

// sharedMemoryOut is an output that only reaches a consumer intact when
// producer and consumer are the same process: encoding/json drops
// Handle, so anything downstream reads a nil pointer.
type sharedMemoryOut struct {
	Tag    string  `json:"tag"`
	Handle *string `json:"-"`
}

type sharedMemoryBuild struct {
	sparkwing.Base
	sparkwing.Produces[sharedMemoryOut]
}

func (j *sharedMemoryBuild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (sharedMemoryBuild) run(ctx context.Context) (sharedMemoryOut, error) {
	live := "only-in-this-process"
	return sharedMemoryOut{Tag: "v9", Handle: &live}, nil
}

type sharedMemoryDeploy struct {
	sparkwing.Base
	Build sparkwing.Ref[sharedMemoryOut]
}

func (d *sharedMemoryDeploy) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", d.run)
	return nil, nil
}

func (d *sharedMemoryDeploy) run(ctx context.Context) error {
	got := d.Build.Get(ctx)
	if got.Tag != "v9" {
		return fmt.Errorf("ref got tag %q, want v9", got.Tag)
	}
	if got.Handle == nil {
		return fmt.Errorf("handle did not survive the ref")
	}
	return nil
}

type sharedMemoryPipe struct{ sparkwing.Base }

func (sharedMemoryPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", &sharedMemoryBuild{})
	sparkwing.Job(plan, "deploy", &sharedMemoryDeploy{Build: sparkwing.RefTo[sharedMemoryOut](build)}).Needs(build)
	return nil
}

func init() {
	register("orch-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &okPipe{} })
	register("orch-fail", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &failPipe{} })
	register("orch-fanout-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &fanOutOK{} })
	register("orch-middle-fails", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &middleFails{} })
	register("orch-ref", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &refPipe{} })
	register("orch-ref-shared-memory", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &sharedMemoryPipe{} })
	register("orch-cancel-inflight", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cancelInflight{} })
}

// newPaths returns a Paths under t.TempDir() with the root created.
func newPaths(t *testing.T) orchestrator.Paths {
	t.Helper()
	isolateProfiles(t)
	root := t.TempDir()
	p := orchestrator.PathsAt(root)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	return p
}

// isolateProfiles points profile resolution at an empty profiles.yaml and
// neutralizes the detect env vars, so a run/read in tests resolves the
// built-in laptop profile (local sqlite) rather than the developer's real
// ~/.config/sparkwing/profiles.yaml.
func isolateProfiles(t *testing.T) {
	t.Helper()
	t.Setenv("SPARKWING_PROFILES", filepath.Join(t.TempDir(), "profiles.yaml"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
}

func TestRun_SingleJobSuccess(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success", res.Status)
	}
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	body, err := os.ReadFile(p.NodeLog(res.RunID, "orch-ok"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(body), "work complete") {
		t.Fatalf("log missing message: %s", body)
	}
}

func TestRun_FailurePropagatesResult(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-fail"})
	if err != nil {
		t.Fatalf("Run returned err=%v; failures should surface via Result not err", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if res.Error == nil {
		t.Fatal("expected Result.Error for failed run")
	}
}

// Bare errors.New("boom") from a node body must be prefixed with the
// node ID by dispatch, so failure surfaces identify the failing node
// without authors having to wrap manually.
func TestRun_FailureAutoWrapsErrorWithNodeID(t *testing.T) {
	p := newPaths(t)
	res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-fail"})

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	got := nodes[0].Error
	want := "orch-fail:"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("node error = %q, want it to start with %q (node ID prefix)", got, want)
	}
	if !strings.Contains(got, "boom") {
		t.Fatalf("node error = %q, expected the original message to survive the wrap", got)
	}
}

func TestRun_FanOutFanIn(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-fanout-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v)", res.Status, res.Error)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(nodes))
	}
	for _, n := range nodes {
		if n.Outcome != string(sparkwing.Success) {
			t.Fatalf("node %q outcome %q, want success", n.NodeID, n.Outcome)
		}
	}
}

func TestRun_MidFailureCancelsDownstream(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-middle-fails"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	byID := map[string]*store.Node{}
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	if byID["a"].Outcome != string(sparkwing.Success) {
		t.Fatalf("a outcome = %q", byID["a"].Outcome)
	}
	if byID["b"].Outcome != string(sparkwing.Failed) {
		t.Fatalf("b outcome = %q", byID["b"].Outcome)
	}
	if byID["c"].Outcome != string(sparkwing.Cancelled) {
		t.Fatalf("c outcome = %q, want cancelled", byID["c"].Outcome)
	}
	if !strings.Contains(byID["c"].Error, "upstream-failed") {
		t.Fatalf("c should cite upstream-failed, got %q", byID["c"].Error)
	}
}

func TestRun_InFlightNodeCanceledByRunRecordedCancelledNotFailed(t *testing.T) {
	p := newPaths(t)
	inflightCancelStarted = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-inflightCancelStarted
		cancel()
	}()

	res, err := orchestrator.RunLocal(ctx, p, orchestrator.Options{Pipeline: "orch-cancel-inflight"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	var victim *store.Node
	for _, n := range nodes {
		if n.NodeID == "victim" {
			victim = n
		}
	}
	if victim == nil {
		t.Fatal("victim node not found")
	}
	if victim.Outcome != string(sparkwing.Cancelled) {
		t.Fatalf("victim outcome = %q, want cancelled (killed by run teardown, not an independent failure)", victim.Outcome)
	}
	if strings.Contains(victim.Error, "context canceled") || strings.Contains(victim.Error, "signal: killed") {
		t.Fatalf("victim carries a scary teardown error %q; a cancelled node should not read as a fault", victim.Error)
	}
}

func TestRun_TypedRefsThreadOutput(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ref"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v)", res.Status, res.Error)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	for _, n := range nodes {
		if n.NodeID != "build" {
			continue
		}
		var out refBuildOut
		if err := json.Unmarshal(n.Output, &out); err != nil {
			t.Fatalf("unmarshal build output: %v (%s)", err, n.Output)
		}
		if out.Tag != "v9" {
			t.Fatalf("build output %q, want v9", out.Tag)
		}
	}
}

// A pipeline that only works because two nodes shared memory has to
// break where its author runs it. This run executes nodes inside the
// test binary -- the shape every `go test` of a pipeline has, and the
// one a library embedder keeps -- and it still fails, because Ref.Get
// resolves from the node's stored JSON on every execution model there
// is. Passing here and failing in production is the whole class of bug
// process-per-node exists to close.
func TestRun_InProcessRefsStillCrossAJSONBoundary(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-ref-shared-memory"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed: the producer's pointer cannot reach the consumer", res.Status)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	deploy, err := st.GetNode(context.Background(), res.RunID, "deploy")
	if err != nil || deploy == nil {
		t.Fatalf("get deploy node: %v", err)
	}
	if !strings.Contains(deploy.Error, "handle did not survive the ref") {
		t.Fatalf("deploy error = %q, want the consumer's own complaint about the dropped pointer", deploy.Error)
	}
	// safety: the rest of the output has to arrive, or this test would
	// pass on a ref that resolved nothing at all.
	build, err := st.GetNode(context.Background(), res.RunID, "build")
	if err != nil || build == nil {
		t.Fatalf("get build node: %v", err)
	}
	if string(build.Output) != `{"tag":"v9"}` {
		t.Fatalf("build output = %s, want the marshaled output the ref resolves from", build.Output)
	}
}

func TestRun_PersistsPlanSnapshotAndRunRow(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-fanout-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()

	r, err := st.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Pipeline != "orch-fanout-ok" {
		t.Fatalf("pipeline = %q", r.Pipeline)
	}
	if r.Status != "success" {
		t.Fatalf("status = %q", r.Status)
	}
	if r.FinishedAt == nil || r.FinishedAt.Before(r.StartedAt) {
		t.Fatalf("finished_at not set properly")
	}
	if len(r.PlanSnapshot) == 0 {
		t.Fatal("plan snapshot not persisted")
	}
}

func TestRun_RetryPlanDriftFailsBeforeCreatingNodes(t *testing.T) {
	p := newPaths(t)
	const runID = "retry-plan-drift"
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:          "orch-fanout-ok",
		RunID:             runID,
		RetryOf:           "source-run",
		RetryRepoDir:      "/recorded/repo-a",
		RetryRepoIdentity: "git@example.test:owner/repo-a.git",
		RetryRevision:     "abc123",
		RetryPlanHash:     "sha256:not-the-source-plan",
	})
	if err != nil {
		t.Fatalf("RunLocal setup: %v", err)
	}
	if res.Status != "failed" || res.Error == nil || !strings.Contains(res.Error.Error(), "retry provenance drift") {
		t.Fatalf("result=%+v, want retry provenance drift failure", res)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	nodes, err := st.ListNodes(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("retry plan drift created %d nodes; no step may start", len(nodes))
	}
	run, err := st.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	prov, _ := run.Invocation["retry_provenance"].(map[string]any)
	if got := prov["repo_dir"]; got != "/recorded/repo-a" {
		t.Fatalf("stored retry repo provenance=%v", got)
	}
	if got := prov["repo_identity"]; got != "git@example.test:owner/repo-a.git" {
		t.Fatalf("stored retry repository identity=%v", got)
	}
	if got := prov["revision"]; got != "abc123" {
		t.Fatalf("stored retry revision=%v", got)
	}
	if got := prov["content_policy"]; got != retryprovenance.RecordedRevisionSnapshotPolicy {
		t.Fatalf("stored retry content policy=%v", got)
	}
}

func TestRun_RetryAcceptsMatchingSourcePlanSnapshot(t *testing.T) {
	p := newPaths(t)
	const sourceID = "retry-plan-source"
	source, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "orch-fanout-ok",
		RunID:    sourceID,
	})
	if err != nil || source.Status != "success" {
		t.Fatalf("source run=%+v err=%v", source, err)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	sourceRun, err := st.GetRun(context.Background(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	sum := sha256.Sum256(sourceRun.PlanSnapshot)

	retry, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:      "orch-fanout-ok",
		RunID:         "retry-plan-match",
		RetryOf:       sourceID,
		RetryPlanHash: fmt.Sprintf("sha256:%x", sum),
	})
	if err != nil {
		t.Fatalf("retry setup: %v", err)
	}
	if retry.Status != "success" {
		t.Fatalf("matching source plan rejected: %+v", retry)
	}
}

func TestRun_UnknownPipelineErrors(t *testing.T) {
	p := newPaths(t)
	_, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "nope-not-registered"})
	if err == nil {
		t.Fatal("expected error for unknown pipeline")
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestRun_PathsIsolation(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entries, err := os.ReadDir(p.RunDir(res.RunID))
	if err != nil {
		t.Fatalf("read run dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("run dir empty")
	}
	if !strings.HasSuffix(filepath.Base(p.NodeLog(res.RunID, "orch-ok")), ".log") {
		t.Fatal("log name convention broken")
	}
	_ = time.Millisecond
}
