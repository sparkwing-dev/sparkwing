//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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
		Session:  SessionConfig{Entry: pipelineentry.Binary(bin), Env: []string{"GENFIXTURE_MODE=" + mode, "GENFIXTURE_TRACE=" + trace}, Grace: 20 * time.Millisecond, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}},
		Pipeline: "probe", Run: nodeproto.RunContext{RunID: "run-probe"}, State: st, Logger: slog.New(slog.DiscardHandler),
		Attempts: &Attempts{
			Routes: routes, Local: st, Resolver: fixtureResolver{}, Masker: secrets.NewMasker(),
			OpenLog: func(_ context.Context, _, node string) (NodeLog, error) { return memLog{logs: logs, node: node}, nil },
		},
	}
	if err := st.CreateRun(t.Context(), store.Run{ID: "run-probe", Pipeline: "probe", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	source := nodeproto.JobSpec{ID: "targets", Steps: []nodeproto.StepSpec{{ID: "body"}}, ResultStep: "body", Closures: nodeproto.ClosureSpec{Generate: []string{"targets/generate/0"}}, TimeoutMS: new(int64(20))}
	run := &Run{ID: "run-probe", Doc: nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{source}}, State: st, Logger: cfg.Logger, validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	s := &scheduler{run: run, results: map[string]Result{}, started: map[string]bool{}}
	if err := s.add(t.Context(), t.Context(), []nodeproto.JobSpec{source}, nil); err != nil {
		t.Fatal(err)
	}
	child := nodeproto.JobSpec{ID: "child", Steps: []nodeproto.StepSpec{{ID: "body"}}, ResultStep: "body", Groups: []string{"targets/generate/0"}, Needs: []string{"targets"}, TimeoutMS: new(int64(20))}
	if mode == "nested" {
		child.Closures.Generate = []string{"child/generate/0"}
	}
	children := []nodeproto.JobSpec{child, {ID: "recover", Steps: []nodeproto.StepSpec{{ID: "body"}}, ResultStep: "body", Groups: []string{"targets/generate/0"}, OnFailureOf: "child"}}
	if err := s.addGenerated(t.Context(), "targets", expansion{closure: "targets/generate/0", nodes: children}); err != nil {
		t.Fatal(err)
	}
	if mode == "nested" {
		grandchild := []nodeproto.JobSpec{{ID: "grandchild", Steps: []nodeproto.StepSpec{{ID: "body"}}, ResultStep: "body", Groups: []string{"child/generate/0"}, Needs: []string{"child"}}}
		if err := s.addGenerated(t.Context(), "child", expansion{closure: "child/generate/0", nodes: grandchild}); err != nil {
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
	if got := strings.Join(evals(t, trace), ","); got != "targets/generate/0,child/generate/0" {
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
		run:     &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}},
		results: map[string]Result{}, started: map[string]bool{},
	}
	if err := first.add(ctx, ctx, doc.Jobs, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNodeWithReason(ctx, testRunID, "targets", sourceOutcome, "", []byte(`"from-targets"`), "", nil); err != nil {
		t.Fatal(err)
	}
	if childOutcome != "" {
		child := nodeproto.JobSpec{ID: "test-a", Needs: deps("targets"), Steps: []nodeproto.StepSpec{{ID: "body"}}}
		if err := first.addGenerated(ctx, "targets", expansion{closure: testGenerator, nodes: []nodeproto.JobSpec{child}}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishNodeWithReason(ctx, testRunID, "test-a", childOutcome, "test failure", nil, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{Dispatch: remoteHost(st, sparkwing.Success, "", nil)}
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	generated := 0
	s := &scheduler{
		run: run, results: map[string]Result{}, started: map[string]bool{}, exec: chain(&cfg), resume: true,
		regenerate: func(_ context.Context, n *NodeRun, _ json.RawMessage) []expansion {
			generated++
			return []expansion{{closure: testGenerator, nodes: []nodeproto.JobSpec{{ID: "test-a", Needs: deps(n.Plan.ID), Steps: []nodeproto.StepSpec{{ID: "body"}}}}}}
		},
	}
	if err := s.add(ctx, ctx, doc.Jobs, nil); err != nil {
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

func outputHost(t *testing.T, scenario string) (*Config, *store.Store) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a protocol fixture")
	}
	bin := filepath.Join(t.TempDir(), "genoutputfixture")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/genoutputfixture").CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	st := openStore(t)
	routes, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = routes.Close() })
	logs := &memLogs{records: map[string][]sparkwing.LogRecord{}}
	cfg := &Config{
		Pipeline: "probe", Run: nodeproto.RunContext{RunID: "run-output"}, State: st, Logger: slog.New(slog.DiscardHandler),
		Session: SessionConfig{Entry: pipelineentry.Binary(bin), Env: []string{"GENFIXTURE_SCENARIO=" + scenario}, Grace: 20 * time.Millisecond, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}},
		Attempts: &Attempts{
			Routes: routes, Local: st, Resolver: fixtureResolver{}, Masker: secrets.NewMasker(),
			OpenLog: func(_ context.Context, _, id string) (NodeLog, error) { return memLog{logs: logs, node: id}, nil },
		},
	}
	cfg.Dispatch = func(ctx context.Context, n *NodeRun) Result {
		return HostDispatchedNode(ctx, *cfg, n.Plan.ID, jobHash(n.Plan))
	}
	return cfg, st
}

// safety: a generated recovery node's row descends from its parent, so the
// generator that loads its code may read the source's output, and it runs.
func TestAGeneratedRecoveryReadsItsSourcesOutput(t *testing.T) {
	cfg, st := outputHost(t, "recovery")
	res, err := Execute(t.Context(), *cfg)
	if err != nil {
		t.Fatal(err)
	}
	row, err := st.GetNode(t.Context(), cfg.Run.RunID, "recover")
	if err != nil {
		t.Fatal(err)
	}
	if rec := res.Nodes["recover"]; rec.Outcome != sparkwing.Success || !slices.Contains(row.Deps, "child") {
		t.Fatalf("recover = %s (%v), row deps %v; want success under its parent", rec.Outcome, rec.Err, row.Deps)
	}
}

func TestANestedGeneratorReadsItsSourcesOutput(t *testing.T) {
	cfg, _ := outputHost(t, "nested")
	res, err := Execute(t.Context(), *cfg)
	if err != nil || res.Status != "success" || res.Nodes["grandchild"].Outcome != sparkwing.Success {
		t.Fatalf("nested run = %s (%v), grandchild %s: %v", res.Status, res.Err, res.Nodes["grandchild"].Outcome, err)
	}
}

// safety: a retry copies a complete recorded batch even when a sibling
// generator recorded none, so the failed child it holds runs again rather
// than vanishing when the first generator now returns nothing.
func TestARetryKeepsACompleteBatchBesideAnEmptyOne(t *testing.T) {
	cfg, st := outputHost(t, "multi")
	first, err := Execute(t.Context(), *cfg)
	if err != nil || first.Status != "failed" || first.Nodes["child"].Outcome != sparkwing.Failed {
		t.Fatalf("source run = %s, child %s: %v", first.Status, first.Nodes["child"].Outcome, err)
	}
	cfg.Rehydrate = &Rehydration{SourceRunID: cfg.Run.RunID}
	cfg.Run.RunID = "run-output-retry"
	cfg.Session.Env = []string{"GENFIXTURE_SCENARIO=multi", "GENFIXTURE_PHASE=omitted"}
	retry, err := Execute(t.Context(), *cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, rowErr := st.GetNode(t.Context(), cfg.Run.RunID, "child"); retry.Status == "success" || rowErr != nil {
		t.Fatalf("retry = %s (%v), child row %v; want the recorded child kept and failing", retry.Status, retry.Err, rowErr)
	}
}

func twoGeneratorDoc() nodeproto.PlanDoc {
	doc := fanOutDoc()
	doc.Jobs[0].Closures.Generate = append(doc.Jobs[0].Closures.Generate, secondGenerator)
	doc.Jobs[1].NeedsGenerated = append(doc.Jobs[1].NeedsGenerated, secondGenerator)
	return doc
}

const secondGenerator = "targets/generate/1"

func resumeAfter(t *testing.T, doc nodeproto.PlanDoc, record func(*scheduler), regen []expansion) (*scheduler, *store.Store, *int) {
	t.Helper()
	st := openStore(t)
	ctx := t.Context()
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	first := &scheduler{run: run, results: map[string]Result{}, started: map[string]bool{}}
	if err := first.add(ctx, ctx, doc.Jobs, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNodeWithReason(ctx, testRunID, "targets", "success", "", []byte(`"targets"`), "", nil); err != nil {
		t.Fatal(err)
	}
	record(first)
	calls := 0
	cfg := Config{Dispatch: remoteHost(st, sparkwing.Success, "", nil)}
	s := &scheduler{
		run: run, results: map[string]Result{}, started: map[string]bool{}, exec: chain(&cfg), resume: true,
		regenerate: func(context.Context, *NodeRun, json.RawMessage) []expansion { calls++; return regen },
	}
	if err := s.add(ctx, ctx, doc.Jobs, nil); err != nil {
		t.Fatal(err)
	}
	s.loop(ctx)
	return s, st, &calls
}

var (
	childA = nodeproto.JobSpec{ID: "test-a", Needs: deps("targets"), Steps: []nodeproto.StepSpec{{ID: "body"}}}
	childB = nodeproto.JobSpec{ID: "test-b", Needs: deps("targets"), Steps:

	// safety: one generator's complete batch is kept as recorded while only its
	// sibling's, which recorded nothing, is generated, so a failed child stays.
	[]nodeproto.StepSpec{{ID: "body"}}}
)

func TestAResumeKeepsACompleteBatchBesideAnEmptyOne(t *testing.T) {
	s, _, calls := resumeAfter(t, twoGeneratorDoc(), func(first *scheduler) {
		if err := first.addGenerated(t.Context(), "targets", expansion{closure: testGenerator, nodes: []nodeproto.JobSpec{childA}}); err != nil {
			t.Fatal(err)
		}
		if err := first.run.State.FinishNodeWithReason(t.Context(), testRunID, "test-a", "failed", "accepted test failed", nil, "", nil); err != nil {
			t.Fatal(err)
		}
	}, []expansion{{closure: testGenerator}, {closure: secondGenerator}})
	if status, err := s.verdict(t.Context()); status != "failed" || *calls != 1 || s.results["test-a"].Outcome != sparkwing.Failed {
		t.Fatalf("resumed run = %s (%v), generator runs %d, test-a %s; want failed with test-a kept", status, err, *calls, s.results["test-a"].Outcome)
	}
}

func partialBatch(t *testing.T) func(*scheduler) {
	return func(first *scheduler) {
		row := pendingRow(first.run, childA)
		if err := (&expansionFrom{source: "targets", closure: testGenerator}).record(&row, childA, 2); err != nil {
			t.Fatal(err)
		}
		if err := first.run.State.CreateNode(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}
}

// An interrupted batch is completed from the generator, keeping the row it
// recorded.
func TestAResumeCompletesAnInterruptedBatch(t *testing.T) {
	s, st, calls := resumeAfter(t, fanOutDoc(), partialBatch(t), []expansion{{closure: testGenerator, nodes: []nodeproto.JobSpec{childA, childB}}})
	status, err := s.verdict(t.Context())
	rows, lerr := st.ListNodes(t.Context(), testRunID)
	recorded := 0
	for _, row := range rows {
		if row.Generated != nil {
			recorded++
		}
	}
	if status != "success" || *calls != 1 || lerr != nil || recorded != 2 {
		t.Fatalf("resumed run = %s (%v), generator runs %d, generated rows %d; want both recorded", status, err, *calls, recorded)
	}
}

// safety: a regenerated batch that drops a node the run recorded is plan
// drift, never a smaller fan-out.
func TestARegeneratedBatchThatDropsARecordedNodeDrifts(t *testing.T) {
	s, _, _ := resumeAfter(t, fanOutDoc(), partialBatch(t), []expansion{{closure: testGenerator, nodes: []nodeproto.JobSpec{childB}}})
	status, _ := s.verdict(t.Context())
	summary := s.results["summary"]
	if status != "failed" || summary.Outcome != sparkwing.Cancelled || !strings.Contains(errText(summary.Err), "no longer returns node test-a") {
		t.Fatalf("resumed run = %s, summary %s (%v); want summary cancelled for the drifted batch", status, summary.Outcome, summary.Err)
	}
}

func generatedChain(t *testing.T, st *store.Store, depth int, cycle bool) (*Run, string, string) {
	t.Helper()
	ctx := t.Context()
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: testRunID, Doc: nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "n0", Steps: []nodeproto.StepSpec{{ID: "body"}}}}}, State: st, validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	var last nodeproto.JobSpec
	for i := 1; i <= depth; i++ {
		source := fmt.Sprintf("n%d", i-1)
		if cycle && i == 1 {
			source = fmt.Sprintf("n%d", depth)
		}
		last = nodeproto.JobSpec{ID: fmt.Sprintf("n%d", i), Needs: deps(source), Steps: []nodeproto.StepSpec{{ID: "body"}}}
		row := pendingRow(run, last)
		if err := (&expansionFrom{source: source, closure: source + "/generate/0"}).record(&row, last, 1); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateNode(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	return run, last.ID, jobHash(last)
}

// A pod follows a long chain of generated sources back to the plan.
func TestAPodFollowsALongGeneratorChain(t *testing.T) {
	run, id, hash := generatedChain(t, openStore(t), 70, false)
	_, chain, err := podGenerated(t.Context(), run, id, hash)
	if err != nil || len(chain) != 70 || chain[0] != "n0/generate/0" {
		t.Fatalf("chain of %d (%v), first %v: want 70 generators from n0's", len(chain), err, chain[:min(1, len(chain))])
	}
}

// safety: recorded sources that loop back are refused rather than followed.
func TestAPodRefusesACycleOfRecordedSources(t *testing.T) {
	run, id, hash := generatedChain(t, openStore(t), 3, true)
	_, _, err := podGenerated(t.Context(), run, id, hash)
	var perr *apierr.Error
	if !errors.As(err, &perr) || perr.Code != apierr.PlanDrift {
		t.Fatalf("cyclic sources = %v, want plan_drift", err)
	}
}

func onlySource() nodeproto.PlanDoc {
	doc := fanOutDoc()
	doc.Jobs = doc.Jobs[:1]
	return doc
}

// safety: a recorded batch the generator no longer reproduces fails the run
// even with no node waiting on the generator, and its recorded node stays
// visible with a terminal row rather than pending or gone.
func TestADriftedBatchWithoutAWaiterFailsTheRun(t *testing.T) {
	changedA := childA
	changedA.Optional = true
	for name, regen := range map[string][]nodeproto.JobSpec{"dropped": {childB}, "changed": {changedA, childB}} {
		t.Run(name, func(t *testing.T) {
			s, st, _ := resumeAfter(t, onlySource(), partialBatch(t), []expansion{{closure: testGenerator, nodes: regen}})
			status, err := s.verdict(t.Context())
			row := nodeRow(t, st, "test-a")
			if status != "failed" || !strings.Contains(errText(err), "plan_drift") || row.Status != "done" {
				t.Fatalf("resumed run = %s (%v), test-a %s/%s; want failed for drift with test-a ended", status, err, row.Status, row.Outcome)
			}
		})
	}
}

// safety: a retry whose copied source's recorded batch no longer regenerates
// fails, and the recorded node still gets its row in the retry.
func TestARetryOfADriftedBatchWithoutAWaiterFails(t *testing.T) {
	st := openStore(t)
	ctx := t.Context()
	doc := onlySource()
	if err := st.CreateRun(ctx, store.Run{ID: sourceRunID, Pipeline: "p", Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	src := &scheduler{run: &Run{ID: sourceRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}, results: map[string]Result{}, started: map[string]bool{}}
	if err := src.add(ctx, ctx, doc.Jobs, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNodeWithReason(ctx, sourceRunID, "targets", "success", "", []byte(`"targets"`), "", nil); err != nil {
		t.Fatal(err)
	}
	partialBatch(t)(src)
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Dispatch: remoteHost(st, sparkwing.Success, "", nil)}
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	s := &scheduler{
		run: run, results: map[string]Result{}, started: map[string]bool{}, exec: chain(&cfg),
		rehydrate: &Rehydration{SourceRunID: sourceRunID}, rerun: map[string]bool{},
		regenerate: func(context.Context, *NodeRun, json.RawMessage) []expansion {
			return []expansion{{closure: testGenerator, nodes: []nodeproto.JobSpec{childB}}}
		},
	}
	if err := s.add(ctx, ctx, doc.Jobs, nil); err != nil {
		t.Fatal(err)
	}
	s.loop(ctx)
	status, err := s.verdict(ctx)
	if _, rowErr := st.GetNode(ctx, testRunID, "test-a"); status != "failed" || rowErr != nil {
		t.Fatalf("retry = %s (%v), test-a row %v; want failed with the recorded node kept", status, err, rowErr)
	}
}

// perf: recovering an incomplete batch evaluates only its own generator, so a
// complete sibling's generator neither runs again nor can abort the recovery.
func TestRecoveringABatchEvaluatesOnlyItsGenerator(t *testing.T) {
	var asked []string
	st := openStore(t)
	ctx := t.Context()
	doc := twoGeneratorDoc()
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	first := &scheduler{run: run, results: map[string]Result{}, started: map[string]bool{}}
	if err := first.add(ctx, ctx, doc.Jobs, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNodeWithReason(ctx, testRunID, "targets", "success", "", []byte(`"targets"`), "", nil); err != nil {
		t.Fatal(err)
	}
	if err := first.addGenerated(ctx, "targets", expansion{closure: testGenerator, nodes: []nodeproto.JobSpec{childA}}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Dispatch: remoteHost(st, sparkwing.Success, "", nil)}
	s := &scheduler{
		run: run, results: map[string]Result{}, started: map[string]bool{}, exec: chain(&cfg), resume: true,
		regenerate: func(_ context.Context, n *NodeRun, _ json.RawMessage) []expansion {
			asked = append(asked, n.Plan.Closures.Generate...)
			return []expansion{{closure: secondGenerator}}
		},
	}
	if err := s.add(ctx, ctx, doc.Jobs, nil); err != nil {
		t.Fatal(err)
	}
	s.loop(ctx)
	if status, err := s.verdict(ctx); status != "success" || !slices.Equal(asked, []string{secondGenerator}) {
		t.Fatalf("resumed run = %s (%v), generators evaluated %v; want success evaluating only %s", status, err, asked, secondGenerator)
	}
}
