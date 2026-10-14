package nodehost

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const sourceRunID = "run-source"

func sourceRun(t *testing.T, st *store.Store, outcomes map[string]string) {
	t.Helper()
	ctx := t.Context()
	if err := st.CreateRun(ctx, store.Run{ID: sourceRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	for id, outcome := range outcomes {
		if err := st.CreateNode(ctx, store.Node{RunID: sourceRunID, NodeID: id, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		var output []byte
		errMsg := "broke"
		if outcome == string(sparkwing.Success) {
			output, errMsg = []byte(`"from-`+id+`"`), ""
		}
		if err := st.FinishNodeWithReason(ctx, sourceRunID, id, outcome, errMsg, output, "", nil); err != nil {
			t.Fatal(err)
		}
	}
}

func rehydratedRun(t *testing.T, st *store.Store, r *Rehydration, doc nodeproto.PlanDoc) (*fakeExec, RunResult) {
	t.Helper()
	f := &fakeExec{}
	return f, hostWith(t, t.Context(), st, Config{Execute: f.run, Rehydrate: r}, doc)
}

func skippedFromRetry(t *testing.T, st *store.Store) []string {
	t.Helper()
	events, err := st.ListEventsAfter(context.Background(), testRunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, ev := range events {
		if ev.Kind == "node_skipped_from_retry" {
			ids = append(ids, ev.NodeID)
		}
	}
	slices.Sort(ids)
	return ids
}

func TestARetryCopiesWhatItsSourceRunAlreadySucceededAt(t *testing.T) {
	st := openStore(t)
	sourceRun(t, st, map[string]string{"a": "success", "b": "failed", "c": "success"})
	doc := nodeproto.PlanDoc{Pipeline: "p", Nodes: []nodeproto.PlanNode{{ID: "a"}, {ID: "b"}, {ID: "c", Deps: []string{"a"}}}}
	f, res := rehydratedRun(t, st, &Rehydration{SourceRunID: sourceRunID}, doc)
	if res.Status != "success" || !slices.Equal(f.calls, []string{"b"}) {
		t.Fatalf("status %s ran %v, want success running only b, which failed in the source run", res.Status, f.calls)
	}
	if got := skippedFromRetry(t, st); !slices.Equal(got, []string{"a", "c"}) {
		t.Errorf("node_skipped_from_retry for %v, want a and c", got)
	}
	out, err := st.GetNodeOutput(t.Context(), testRunID, "a")
	if err != nil || string(out) != `"from-a"` {
		t.Errorf("copied output %q, %v; want the source run's", out, err)
	}
}

func TestAnAutomaticRetryRunsOnlyItsCausesAndWhatTheyReach(t *testing.T) {
	st := openStore(t)
	sourceRun(t, st, map[string]string{"a": "success", "b": "failed", "c": "cancelled", "d": "failed", "rescue": "skipped"})
	doc := nodeproto.PlanDoc{Pipeline: "p", Nodes: []nodeproto.PlanNode{
		{ID: "a"},
		{ID: "b"},
		{ID: "c", Deps: []string{"b"}},
		{ID: "d", Deps: []string{"a"}},
		{ID: "rescue", OnFailureOf: "b"},
	}}
	f, res := rehydratedRun(t, st, &Rehydration{SourceRunID: sourceRunID, Causes: []string{"b"}}, doc)
	slices.Sort(f.calls)
	if !slices.Equal(f.calls, []string{"b", "c"}) {
		t.Fatalf("ran %v, want the cause b and its dependent c", f.calls)
	}
	if got := skippedFromRetry(t, st); !slices.Equal(got, []string{"a", "d"}) {
		t.Errorf("node_skipped_from_retry for %v, want a and d", got)
	}
	if d := res.Nodes["d"]; d.Outcome != sparkwing.Failed {
		t.Errorf("d = %s, want its source failure copied: an automatic retry copies every node it does not name", d.Outcome)
	}
	if r := res.Nodes["rescue"]; r.Outcome != sparkwing.Skipped {
		t.Errorf("rescue = %s, want skipped, as b succeeded on the retry", r.Outcome)
	}
}

type outputGone struct{ *store.Store }

func (outputGone) GetNodeOutput(context.Context, string, string) ([]byte, error) {
	return nil, store.ErrNotFound
}

func scheduled(t *testing.T, st storage.StateStoreOld, cfg Config, doc nodeproto.PlanDoc, regenerate func(context.Context, *NodeRun, json.RawMessage) []expansion) RunResult {
	t.Helper()
	ctx := t.Context()
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest)}
	res := RunResult{RunID: testRunID, Nodes: map[string]Result{}}
	s := &scheduler{
		run: run, results: res.Nodes, started: map[string]bool{}, exec: chain(&cfg),
		rehydrate: cfg.Rehydrate, rerun: map[string]bool{}, regenerate: regenerate,
	}
	if err := s.add(ctx, ctx, doc.Nodes, nil); err != nil {
		t.Fatal(err)
	}
	s.loop(ctx)
	res.Status, res.Err = s.verdict(ctx)
	return res
}

func TestARetryRunsANodeWhoseRecordedOutputIsGone(t *testing.T) {
	st := openStore(t)
	sourceRun(t, st, map[string]string{"a": "success"})
	f := &fakeExec{}
	res := scheduled(t, outputGone{st}, Config{Execute: f.run, Rehydrate: &Rehydration{SourceRunID: sourceRunID}},
		nodeproto.PlanDoc{Pipeline: "p", Nodes: []nodeproto.PlanNode{{ID: "a"}}}, nil)
	if res.Status != "success" || !f.ran("a") {
		t.Fatalf("status %s ran %v: a node whose output is gone must run again, not copy a success with no output", res.Status, f.calls)
	}
}

func TestARetryGeneratesACopiedGeneratorsNodesAgain(t *testing.T) {
	st := openStore(t)
	sourceRun(t, st, map[string]string{"targets": "success", "lint": "success", "summary": "cancelled", "test-a": "failed"})
	doc := fanOutDoc()
	doc.Pipeline = "p"
	f := &fakeExec{}
	var regenerated json.RawMessage
	res := scheduled(t, st, Config{Execute: f.run, Rehydrate: &Rehydration{SourceRunID: sourceRunID}}, doc,
		func(_ context.Context, n *NodeRun, out json.RawMessage) []expansion {
			regenerated = out
			return []expansion{{closure: testGenerator, nodes: []nodeproto.PlanNode{{ID: "test-a", Deps: deps(n.Plan.ID)}}}}
		})
	if res.Status != "success" || f.ran("targets") || !f.ran("test-a") || !f.ran("summary") {
		t.Fatalf("status %s ran %v: the copied generator's node must be generated again and run", res.Status, f.calls)
	}
	if string(regenerated) != `"from-targets"` {
		t.Errorf("regenerated from %q, want the copied output", regenerated)
	}
}

func TestARetryWithoutAnEvalProcessRunsAGeneratorAgain(t *testing.T) {
	st := openStore(t)
	sourceRun(t, st, map[string]string{"targets": "success", "lint": "success", "summary": "cancelled", "test-a": "failed"})
	doc := fanOutDoc()
	doc.Pipeline = "p"
	f := &fakeExec{bodies: map[string]Executor{"targets": generates(nodeproto.PlanNode{ID: "test-a", Deps: deps("targets")})}}
	res := scheduled(t, st, Config{Execute: f.run, Rehydrate: &Rehydration{SourceRunID: sourceRunID}}, doc, nil)
	if res.Status != "success" || !f.ran("targets") || !f.ran("test-a") {
		t.Fatalf("status %s ran %v", res.Status, f.calls)
	}
}

func TestAnAutomaticRetryRunsTheWaiterOfAGeneratorItRuns(t *testing.T) {
	st := openStore(t)
	sourceRun(t, st, map[string]string{"targets": "failed", "lint": "success", "summary": "cancelled"})
	doc := fanOutDoc()
	doc.Pipeline = "p"
	f := &fakeExec{bodies: map[string]Executor{"targets": generates(nodeproto.PlanNode{ID: "test-a", Deps: deps("targets")})}}
	res := scheduled(t, st, Config{Execute: f.run, Rehydrate: &Rehydration{SourceRunID: sourceRunID, Causes: []string{"targets"}}}, doc, nil)
	if res.Status != "success" || !f.ran("summary") || f.ran("lint") {
		t.Fatalf("status %s ran %v: summary waits on the rerun generator's nodes, so it runs; lint copies", res.Status, f.calls)
	}
}
