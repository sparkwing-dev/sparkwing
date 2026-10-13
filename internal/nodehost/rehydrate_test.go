package nodehost

import (
	"context"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
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
