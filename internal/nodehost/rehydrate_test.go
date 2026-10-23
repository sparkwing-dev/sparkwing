package nodehost

import (
	"context"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestARetryCopiesWhatItsSourceRunAlreadySucceededAt(t *testing.T) {
	testRetrySourceProbe(t, runstate.Materialize)
}

func TestARetryRunsANodeWhoseRecordedOutputIsGone(t *testing.T) {
	testRetrySourceProbe(t, runstate.Dispatch)
}

func testRetrySourceProbe(t *testing.T, kind runstate.ReconcileKind) {
	t.Helper()
	f, h, input := newOwnerFixture(t, true)
	row := runstate.NodeRow{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Status: runstate.StatusPending}
	returned := row
	if kind == runstate.Materialize {
		returned.Status, returned.Outcome = runstate.StatusDone, runstate.NodeSuccess
	}
	probe := &ownerReconcileProbe{OwnerStore: f, result: runstate.ReconciledNode{Kind: kind, Node: returned}}
	h.Store = probe
	sourceResult := &runstate.MaterializeSource{Kind: runstate.SourceManualRetry, Result: &runstate.MaterializedResultSource{Ref: runspec.NodeRef{RunID: "previous", NodeID: "job"}, StateSeq: 8}}
	f.snapshot.Run.Retry = &runstate.RetrySelection{Mode: "failed", Results: map[string]runstate.MaterializedResultSource{"job": *sourceResult.Result}}
	setTestPolicies(&h, func(_ context.Context, layer testPolicyName, _ EvaluationInput, _ runstate.NodeRow, _ StorageBinding) (ownerLayerResult, error) {
		if layer.Name == "admission" && kind == runstate.Materialize {
			t.Fatal("copied result entered compute admission")
		}
		return ownerLayerResult{}, nil
	})
	dispatched, err := releasedRequest(&h, t.Context(), input, *input.Accepted, &source.Entry{}, row, store.DecisionReady)
	if err != nil || dispatched != nil || !probe.request.MaterializeOnly || probe.request.Policy != runstate.SourceManualRetry || !reflect.DeepEqual(probe.request.Source, sourceResult) {
		t.Fatalf("probe = %+v, dispatched = %+v, error %v", probe.request, dispatched, err)
	}
	want := []string(nil)
	if kind == runstate.Dispatch {
		want = []string{"ready"}
	}
	if !reflect.DeepEqual(f.trace, want) {
		t.Fatalf("transitions = %v, want %v", f.trace, want)
	}
}
