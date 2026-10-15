package nodehost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/lease"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const testRunID = "run-sched"

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

type fakeExec struct {
	mu     sync.Mutex
	calls  []string
	bodies map[string]Executor
	seen   map[string]*NodeRun
}

func (f *fakeExec) run(ctx context.Context, n *NodeRun) Result {
	f.mu.Lock()
	f.calls = append(f.calls, n.Plan.ID)
	if f.seen == nil {
		f.seen = map[string]*NodeRun{}
	}
	f.seen[n.Plan.ID] = n
	body := f.bodies[n.Plan.ID]
	f.mu.Unlock()
	if body == nil {
		return Result{Outcome: sparkwing.Success}
	}
	return body(ctx, n)
}

func (f *fakeExec) ran(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, id)
}

func hostDoc(t *testing.T, ctx context.Context, st storage.StateStoreOld, doc nodeproto.PlanDocOld, exec Executor) RunResult {
	t.Helper()
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := validateNodes(doc.Nodes); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return hostAccepted(t, ctx, st, doc, exec)
}

func hostAccepted(t *testing.T, ctx context.Context, st storage.StateStoreOld, doc nodeproto.PlanDocOld, exec Executor) RunResult {
	t.Helper()
	cfg := Config{Execute: exec}
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest)}
	res := RunResult{RunID: testRunID, Nodes: map[string]Result{}}
	s := &scheduler{run: run, results: res.Nodes, started: map[string]bool{}, exec: chain(&cfg)}
	if err := s.add(ctx, ctx, doc.Nodes, nil); err != nil {
		t.Fatalf("add: %v", err)
	}
	s.loop(ctx)
	res.Order = s.order
	res.Status, res.Err = s.verdict(ctx)
	return res
}

func nodeRow(t *testing.T, st *store.Store, id string) *store.Node {
	t.Helper()
	row, err := st.GetNode(t.Context(), testRunID, id)
	if err != nil {
		t.Fatalf("node %s: %v", id, err)
	}
	return row
}

func wantRow(t *testing.T, st *store.Store, id string, outcome sparkwing.Outcome, errHas string) {
	t.Helper()
	row := nodeRow(t, st, id)
	if row.Status != "done" || row.Outcome != string(outcome) || !strings.Contains(row.Error, errHas) {
		t.Errorf("node %s row status=%q outcome=%q error=%q, want done/%s containing %q", id, row.Status, row.Outcome, row.Error, outcome, errHas)
	}
}

func deps(ids ...string) []string { return ids }

func TestSchedulerRunsADiamondInOrderAndInParallel(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	bIn, cIn := make(chan struct{}), make(chan struct{})
	meet := func(mine, theirs chan struct{}) Executor {
		return func(ctx context.Context, _ *NodeRun) Result {
			close(mine)
			select {
			case <-theirs:
				return Result{Outcome: sparkwing.Success, Output: []byte(`{"ok":true}`)}
			case <-ctx.Done():
				return Result{Outcome: sparkwing.Failed, Err: errors.New("b and c never ran together")}
			}
		}
	}
	fx := &fakeExec{bodies: map[string]Executor{"b": meet(bIn, cIn), "c": meet(cIn, bIn)}}
	doc := nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{
		{ID: "d", Deps: deps("b", "c")},
		{ID: "b", Deps: deps("a")},
		{ID: "c", Deps: deps("a")},
		{ID: "a"},
	}}

	res := hostDoc(t, ctx, st, doc, fx.run)
	if res.Status != "success" || res.Err != nil {
		t.Fatalf("status=%s err=%v, want success", res.Status, res.Err)
	}
	if len(res.Order) != 4 || res.Order[0] != "a" || res.Order[3] != "d" {
		t.Errorf("order = %v, want a first and d last", res.Order)
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		wantRow(t, st, id, sparkwing.Success, "")
	}
	out, err := st.GetNodeOutput(t.Context(), testRunID, "b")
	if err != nil || string(out) != `{"ok":true}` {
		t.Errorf("b output = %s (err %v), want the executor's output", out, err)
	}
}

func TestSchedulerPropagatesFailureAsTheDispatcherDoes(t *testing.T) {
	st := openStore(t)
	fail := func(context.Context, *NodeRun) Result {
		return Result{Outcome: sparkwing.Failed, Err: errors.New("broke"), Reason: store.FailureVerify}
	}
	fx := &fakeExec{bodies: map[string]Executor{"build": fail, "lint": fail, "flaky": fail, "maybe": fail}}
	doc := nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{
		{ID: "build", Modifiers: &nodeproto.PlanModifiers{OnFailure: "build-recover"}},
		{ID: "test", Deps: deps("build")},
		{ID: "deploy", Deps: deps("test")},
		{ID: "build-recover", OnFailureOf: "build"},
		{ID: "docs"},
		{ID: "docs-recover", OnFailureOf: "docs"},
		{ID: "lint", Modifiers: &nodeproto.PlanModifiers{ContinueOnError: true}},
		{ID: "after-lint", Deps: deps("lint")},
		{ID: "flaky"},
		{ID: "after-flaky", OptionalDeps: deps("flaky", "absent")},
		{ID: "maybe", Modifiers: &nodeproto.PlanModifiers{Optional: true}},
	}}

	res := hostDoc(t, t.Context(), st, doc, fx.run)

	wantRow(t, st, "build", sparkwing.Failed, "broke")
	wantRow(t, st, "test", sparkwing.Cancelled, "upstream-failed")
	wantRow(t, st, "deploy", sparkwing.Cancelled, "upstream-failed")
	wantRow(t, st, "build-recover", sparkwing.Success, "")
	wantRow(t, st, "docs-recover", sparkwing.Skipped, `parent "docs" did not fail`)
	wantRow(t, st, "after-lint", sparkwing.Success, "")
	wantRow(t, st, "after-flaky", sparkwing.Cancelled, "upstream-failed")
	wantRow(t, st, "maybe", sparkwing.Failed, "broke")
	for _, id := range []string{"test", "deploy", "docs-recover", "after-flaky"} {
		if fx.ran(id) {
			t.Errorf("node %s ran", id)
		}
	}
	if f := fx.seen["build-recover"].Failure; f == nil || *f != (nodeproto.Failure{Node: "build", Stage: "verify", Message: "broke"}) {
		t.Errorf("recovery failure = %+v, want build's verify failure", f)
	}
	want := "nodes failed: [build lint flaky]; 3 more cancelled with the run"
	if res.Status != "failed" || res.Err == nil || res.Err.Error() != want {
		t.Errorf("status=%s err=%v, want failed with %q", res.Status, res.Err, want)
	}
}

func TestSchedulerOptionalFailureLeavesTheRunGreen(t *testing.T) {
	st := openStore(t)
	fx := &fakeExec{bodies: map[string]Executor{"maybe": func(context.Context, *NodeRun) Result {
		return Result{Outcome: sparkwing.Failed, Err: errors.New("broke")}
	}}}
	doc := nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{
		{ID: "maybe", Modifiers: &nodeproto.PlanModifiers{Optional: true}},
		{ID: "after", OptionalDeps: deps("absent")},
	}}
	res := hostDoc(t, t.Context(), st, doc, fx.run)
	if res.Status != "success" {
		t.Errorf("status=%s err=%v, want success: an optional node's failure does not fail the run", res.Status, res.Err)
	}
	wantRow(t, st, "after", sparkwing.Success, "")
}

func TestSchedulerCancelMidRunFinishesEveryRow(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	fx := &fakeExec{bodies: map[string]Executor{"slow": func(ctx context.Context, _ *NodeRun) Result {
		cancel(&lease.CancelError{Reason: "interrupted by SIGINT"})
		<-ctx.Done()
		return Result{Outcome: sparkwing.Failed, Err: ctx.Err()}
	}}}
	doc := nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{
		{ID: "slow"},
		{ID: "next", Deps: deps("slow")},
		{ID: "later", Deps: deps("next")},
	}}

	res := hostDoc(t, ctx, st, doc, fx.run)
	if res.Status != "cancelled" || res.Err == nil || res.Err.Error() != "interrupted by SIGINT" {
		t.Errorf("status=%s err=%v, want cancelled with the cause", res.Status, res.Err)
	}
	wantRow(t, st, "slow", sparkwing.Cancelled, "cancelled: run failing")
	wantRow(t, st, "next", sparkwing.Cancelled, "ctx-cancelled")
	wantRow(t, st, "later", sparkwing.Cancelled, "ctx-cancelled")
	if fx.ran("next") || fx.ran("later") {
		t.Error("a node started after the run was cancelled")
	}
}

func TestSchedulerAStalledPlanFailsItsPendingNodes(t *testing.T) {
	st := openStore(t)
	if err := st.CreateRun(t.Context(), store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	fx := &fakeExec{}
	doc := nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{ID: "a", Deps: deps("b")}, {ID: "b", Deps: deps("a")}}}
	cfg := Config{Execute: fx.run}
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler)}
	s := &scheduler{run: run, results: map[string]Result{}, started: map[string]bool{}, exec: chain(&cfg), nodes: doc.Nodes}
	for _, n := range doc.Nodes {
		if err := st.CreateNode(t.Context(), pendingRow(run, n)); err != nil {
			t.Fatal(err)
		}
	}
	s.loop(t.Context())
	if len(s.order) != 0 || len(fx.calls) != 0 {
		t.Fatalf("started %v from a plan where nothing is ready", s.order)
	}
	wantRow(t, st, "a", sparkwing.Failed, "never became ready")
	wantRow(t, st, "b", sparkwing.Failed, "never became ready")
}

func TestSchedulerWithoutAnExecutorFailsTheNode(t *testing.T) {
	st := openStore(t)
	res := hostDoc(t, t.Context(), st, nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{ID: "a"}}}, nil)
	if res.Status != "failed" {
		t.Errorf("status = %s, want failed", res.Status)
	}
	wantRow(t, st, "a", sparkwing.Failed, "no attempt executor")
}

func TestSchedulerAddsNodesWhileTheRunGoes(t *testing.T) {
	st := openStore(t)
	var addErrs []error
	fx := &fakeExec{bodies: map[string]Executor{"targets": func(ctx context.Context, n *NodeRun) Result {
		addErrs = append(addErrs,
			n.Run.Add(ctx, nodeproto.PlanNode{ID: "test-linux", Deps: deps("targets")}, nodeproto.PlanNode{ID: "test-darwin", Deps: deps("targets")}),
			n.Run.Add(ctx, nodeproto.PlanNode{ID: "targets"}))
		return Result{Outcome: sparkwing.Success}
	}}}
	res := hostDoc(t, t.Context(), st, nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{ID: "targets"}}}, fx.run)
	if res.Status != "success" || !slices.Equal(res.Order[:1], []string{"targets"}) || len(res.Order) != 3 {
		t.Fatalf("status=%s order=%v, want targets then its two generated nodes", res.Status, res.Order)
	}
	if addErrs[0] != nil {
		t.Errorf("add: %v", addErrs[0])
	}
	var perr *nodeproto.Error
	if !errors.As(addErrs[1], &perr) || perr.Code != nodeproto.CodePlanInvalid {
		t.Errorf("adding a duplicate node: err = %v, want plan_invalid", addErrs[1])
	}
	wantRow(t, st, "test-linux", sparkwing.Success, "")
	wantRow(t, st, "test-darwin", sparkwing.Success, "")
}

func TestSchedulerWritesPendingRowsFromThePlan(t *testing.T) {
	st := openStore(t)
	fx := &fakeExec{bodies: map[string]Executor{"gpu": func(ctx context.Context, n *NodeRun) Result {
		row, err := st.GetNode(ctx, testRunID, n.Plan.ID)
		if err != nil || row.Status != "running" || row.StartedAt == nil {
			return Result{Outcome: sparkwing.Failed, Err: errors.New("row not started before the executor ran")}
		}
		return Result{Outcome: sparkwing.Success}
	}}}
	doc := nodeproto.PlanDocOld{Requires: []string{"linux"}, Nodes: []nodeproto.PlanNode{
		{ID: "base"},
		{ID: "gpu", Deps: deps("base"), OptionalDeps: deps("extra"), Modifiers: &nodeproto.PlanModifiers{
			RunsOn: []string{"gpu", "linux"}, WhenRunner: []string{"big"}, Prefers: []string{"fast"},
			ResCores: 4, ResMemoryBytes: 1 << 30,
		}},
	}}
	res := hostDoc(t, t.Context(), st, doc, fx.run)
	if res.Status != "success" {
		t.Fatalf("status=%s err=%v", res.Status, res.Err)
	}
	row := nodeRow(t, st, "gpu")
	if !slices.Equal(row.Deps, []string{"base", "extra"}) || !slices.Equal(row.NeedsLabels, []string{"gpu", "linux", "big"}) ||
		!slices.Equal(row.PrefersLabels, []string{"fast"}) || row.RequestedCores != 4 || row.RequestedMemoryBytes != 1<<30 {
		t.Errorf("gpu row deps=%v needs=%v prefers=%v cores=%v mem=%d", row.Deps, row.NeedsLabels, row.PrefersLabels, row.RequestedCores, row.RequestedMemoryBytes)
	}
	if base := nodeRow(t, st, "base"); !slices.Equal(base.NeedsLabels, []string{"linux"}) {
		t.Errorf("base needs %v, want the pipeline's requires", base.NeedsLabels)
	}
}

func TestValidateNodesRefusesAnInvalidPlan(t *testing.T) {
	for name, tc := range map[string]struct {
		nodes []nodeproto.PlanNode
		want  string
	}{
		"cycle": {
			nodes: []nodeproto.PlanNode{{ID: "a", Deps: deps("c")}, {ID: "b", Deps: deps("a")}, {ID: "c", Deps: deps("b")}},
			want:  "dependency cycle a -> c -> b -> a",
		},
		"missing dep":      {nodes: []nodeproto.PlanNode{{ID: "a", Deps: deps("ghost")}}, want: "needs ghost"},
		"duplicate node":   {nodes: []nodeproto.PlanNode{{ID: "a"}, {ID: "a"}}, want: "node a twice"},
		"no id":            {nodes: []nodeproto.PlanNode{{}}, want: "no id"},
		"missing recovery": {nodes: []nodeproto.PlanNode{{ID: "r", OnFailureOf: "ghost"}}, want: "recovers ghost"},
		"optional dep cycle": {
			nodes: []nodeproto.PlanNode{{ID: "a", OptionalDeps: deps("b")}, {ID: "b", Deps: deps("a")}},
			want:  "dependency cycle",
		},
		"duplicate closure": {
			nodes: []nodeproto.PlanNode{
				{ID: "a", Closures: &nodeproto.PlanClosures{SkipIf: []string{"x"}}},
				{ID: "b", Work: &nodeproto.PlanWork{Steps: []nodeproto.PlanStep{{ID: "s", Closures: &nodeproto.PlanClosures{Verify: "x"}}}}},
			},
			want: "closure id x belongs to both node a and step b/s",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateNodes(tc.nodes)
			var perr *nodeproto.Error
			if !errors.As(err, &perr) || perr.Code != nodeproto.CodePlanInvalid || !strings.Contains(perr.Message, tc.want) {
				t.Fatalf("err = %v, want plan_invalid naming %q", err, tc.want)
			}
		})
	}
	if err := validateNodes([]nodeproto.PlanNode{{ID: "a", OptionalDeps: deps("absent")}, {ID: "r", OnFailureOf: "a"}}); err != nil {
		t.Errorf("a plan with an absent optional dependency and a recovery node: %v", err)
	}
}

func TestSchedulerDeadlineFailsTheRun(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	fx := &fakeExec{bodies: map[string]Executor{"slow": func(ctx context.Context, _ *NodeRun) Result {
		<-ctx.Done()
		return Result{Outcome: sparkwing.Failed, Err: ctx.Err()}
	}}}
	doc := nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{ID: "slow"}, {ID: "next", Deps: deps("slow")}}}

	res := hostDoc(t, ctx, st, doc, fx.run)
	want := "nodes failed: [slow]; 1 more cancelled with the run"
	if res.Status != "failed" || res.Err == nil || res.Err.Error() != want {
		t.Errorf("status=%s err=%v, want failed with %q", res.Status, res.Err, want)
	}
	wantRow(t, st, "slow", sparkwing.Failed, "deadline exceeded")
	wantRow(t, st, "next", sparkwing.Cancelled, "ctx-cancelled")
}

func TestSchedulerPlainCancelFailsTheRun(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fx := &fakeExec{bodies: map[string]Executor{"a": func(context.Context, *NodeRun) Result {
		cancel()
		return Result{Outcome: sparkwing.Success}
	}}}
	res := hostDoc(t, ctx, st, nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{ID: "a"}}}, fx.run)
	if res.Status != "failed" || !errors.Is(res.Err, context.Canceled) {
		t.Errorf("status=%s err=%v, want failed with the cancel", res.Status, res.Err)
	}
}

type refusingOutput struct{ *store.Store }

func (r refusingOutput) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	if output != nil {
		return fmt.Errorf("%w: over the 64 MiB limit", store.ErrOutputNotStored)
	}
	return r.Store.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
}

func TestSchedulerARefusedOutputFailsTheNodeRow(t *testing.T) {
	st := openStore(t)
	fx := &fakeExec{bodies: map[string]Executor{"big": func(context.Context, *NodeRun) Result {
		return Result{Outcome: sparkwing.Success, Output: []byte(`"huge"`)}
	}}}
	doc := nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{ID: "big"}, {ID: "after", Deps: deps("big")}}}
	res := hostDoc(t, t.Context(), refusingOutput{st}, doc, fx.run)
	if res.Status != "failed" {
		t.Errorf("status=%s err=%v, want failed", res.Status, res.Err)
	}
	wantRow(t, st, "big", sparkwing.Failed, "the node's output was not stored")
	wantRow(t, st, "after", sparkwing.Cancelled, "upstream-failed")
}

// safety: an upload's refusal can quote megabytes of its failed request, and the finish that records it is
// held to the controller's 1 MiB body cap.
type refusingOutputLoudly struct{ *store.Store }

func (r refusingOutputLoudly) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	if output != nil {
		return fmt.Errorf("%w: %s", store.ErrOutputNotStored, strings.Repeat("upload failed\n", 200<<10))
	}
	return cappedFinish(r).FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
}

func TestSchedulerARefusedOutputWithAHugeReasonStillFailsTheNodeRow(t *testing.T) {
	st := openStore(t)
	fx := &fakeExec{bodies: map[string]Executor{"big": func(context.Context, *NodeRun) Result {
		return Result{Outcome: sparkwing.Success, Output: []byte(`"huge"`)}
	}}}
	res := hostDoc(t, t.Context(), refusingOutputLoudly{st}, nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{ID: "big"}}}, fx.run)
	if res.Status != "failed" {
		t.Errorf("status=%s err=%v, want failed", res.Status, res.Err)
	}
	if row := nodeRow(t, st, "big"); row.Outcome != string(sparkwing.Failed) || len(row.Error) > 1<<20 {
		t.Fatalf("row outcome %q with a %d-byte error, want failed with the bounded reason", row.Outcome, len(row.Error))
	}
}

// safety: stands in for the controller's node finish route, which refuses a
// JSON body over 1 MiB.
type cappedFinish struct{ *store.Store }

func (c cappedFinish) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	if len(errMsg) > 1<<20 {
		return errors.New("controller 400: http: request body too large")
	}
	return c.Store.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
}

func TestSchedulerRecordsAFailedCommandWithHugeOutput(t *testing.T) {
	st := openStore(t)
	huge := "command failed (exit 1): go test ./...\n" +
		strings.Repeat("=== RUN   TestSomething\n--- PASS: TestSomething (0.00s)\n", 64<<10) +
		"--- FAIL: TestBroken (0.01s)\nFAIL\tgithub.com/acme/broken\t0.2s\n"
	fx := &fakeExec{bodies: map[string]Executor{"test": func(context.Context, *NodeRun) Result {
		return Result{Outcome: sparkwing.Failed, Err: errors.New(huge)}
	}}}
	res := hostDoc(t, t.Context(), cappedFinish{st}, nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{ID: "test"}}}, fx.run)
	if res.Status != "failed" {
		t.Errorf("status=%s err=%v, want failed", res.Status, res.Err)
	}
	row := nodeRow(t, st, "test")
	if row.Status != "done" || row.Outcome != string(sparkwing.Failed) {
		t.Fatalf("node row status=%q outcome=%q, want done/failed", row.Status, row.Outcome)
	}
	if len(row.Error) > nodeErrTextMaxBytes {
		t.Errorf("recorded error is %d bytes, want at most %d", len(row.Error), nodeErrTextMaxBytes)
	}
	for _, want := range []string{"command failed (exit 1): go test ./...\n", "bytes of this error elided", "FAIL\tgithub.com/acme/broken\t0.2s\n"} {
		if !strings.Contains(row.Error, want) {
			t.Errorf("recorded error lacks %q:\n%s", want, row.Error)
		}
	}
}

type recordingLog struct{ records []sparkwing.LogRecord }

func (l *recordingLog) Emit(rec sparkwing.LogRecord) { l.records = append(l.records, rec) }

// A cut taken before masking would leave a fragment of a secret the masker no
// longer recognizes, so after_run's input keeps the whole error, and a bounded
// recorded error is cut only from text already masked.
func TestNodeErrorBoundingNeverSplitsASecret(t *testing.T) {
	secret := "private-token-ABCDEFGHIJKLMN"
	for name, tc := range map[string]struct{ raw, fragment string }{
		"head": {strings.Repeat("H", 500) + secret + "\n" + strings.Repeat("filler\n", 1800), "private-toke"},
		"tail": {"failed\n" + strings.Repeat("X", 2600) + secret + strings.Repeat("Y", 7540), "HIJKLMN"},
	} {
		t.Run(name, func(t *testing.T) {
			masker := secrets.NewMasker()
			masker.Register(secret)
			arg := afterRunArgOf(Result{Outcome: sparkwing.Failed, Err: errors.New(tc.raw)})
			if arg.Error != tc.raw {
				t.Errorf("after_run input is %d bytes, want the whole %d-byte error", len(arg.Error), len(tc.raw))
			}
			log := &recordingLog{}
			sink, err := NewLogSink(t.Context(), LogSinkConfig{RunID: "run", NodeID: "node", State: noState{}, Log: log, Masker: masker})
			if err != nil {
				t.Fatal(err)
			}
			sink.Record(nodeproto.LogRecord{Level: "error", Msg: arg.Error})
			if len(log.records) != 1 || strings.Contains(log.records[0].Msg, tc.fragment) {
				t.Errorf("a hook logging its error leaked %q: %+v", tc.fragment, log.records)
			}
			recorded := recordedErrText(errors.New(masker.Mask(tc.raw)))
			if strings.Contains(recorded, tc.fragment) || len(recorded) > nodeErrTextMaxBytes {
				t.Errorf("recorded error is %d bytes and leaks %q: %q", len(recorded), tc.fragment, recorded)
			}
		})
	}
}

func TestBoundedNodeErrTextKeepsUTF8AndShortText(t *testing.T) {
	if got := boundedNodeErrText("short"); got != "short" {
		t.Errorf("short text = %q, want it unchanged", got)
	}
	got := boundedNodeErrText(strings.Repeat("é", nodeErrTextMaxBytes))
	if !utf8.ValidString(got) || len(got) > nodeErrTextMaxBytes {
		t.Errorf("bounded text is %d bytes, valid UTF-8 %v", len(got), utf8.ValidString(got))
	}
}

type stallingCreate struct{ *store.Store }

func (s stallingCreate) CreateNode(ctx context.Context, n store.Node) error {
	if n.NodeID == "child" {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.Store.CreateNode(ctx, n)
}

func TestSchedulerAddHonoursTheCallersDeadline(t *testing.T) {
	st := openStore(t)
	var addErr error
	fx := &fakeExec{bodies: map[string]Executor{"gen": func(ctx context.Context, n *NodeRun) Result {
		addCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		addErr = n.Run.Add(addCtx, nodeproto.PlanNode{ID: "child", Deps: deps("gen")})
		return Result{Outcome: sparkwing.Success}
	}}}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	res := hostDoc(t, ctx, stallingCreate{st}, nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{ID: "gen"}}}, fx.run)
	if !errors.Is(addErr, context.DeadlineExceeded) {
		t.Errorf("add err = %v, want the caller's deadline", addErr)
	}
	if res.Status != "success" || ctx.Err() != nil {
		t.Errorf("status=%s err=%v ctx=%v, want the run to finish without its node", res.Status, res.Err, ctx.Err())
	}
}
