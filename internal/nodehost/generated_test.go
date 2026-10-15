//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	generated := 0
	s := &scheduler{
		run: run, results: map[string]Result{}, started: map[string]bool{}, exec: successfulExecution, resume: true,
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
	s := &scheduler{
		run: run, results: map[string]Result{}, started: map[string]bool{}, exec: successfulExecution, resume: true,
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
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	s := &scheduler{
		run: run, results: map[string]Result{}, started: map[string]bool{}, exec: successfulExecution,
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
	s := &scheduler{
		run: run, results: map[string]Result{}, started: map[string]bool{}, exec: successfulExecution, resume: true,
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
