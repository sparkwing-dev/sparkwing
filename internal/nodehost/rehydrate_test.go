package nodehost

import (
	"context"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestARetryCopiesWhatItsSourceRunAlreadySucceededAt(t *testing.T) {
	testRetrySourceProbe(t, store.Materialize)
}

func TestARetryRunsANodeWhoseRecordedOutputIsGone(t *testing.T) {
	testRetrySourceProbe(t, store.Dispatch)
}

func testRetrySourceProbe(t *testing.T, kind store.ReconcileKind) {
	t.Helper()
	f, h, input := newOwnerFixture(t, true)
	row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Status: store.StatusPending}
	returned := row
	if kind == store.Materialize {
		returned.Status, returned.Outcome = store.StatusDone, store.NodeSuccess
	}
	probe := &ownerReconcileProbe{OwnerStore: f, result: store.ReconciledNode{Kind: kind, Node: returned}}
	h.Store = probe
	sourceResult := &store.MaterializeSource{Kind: store.SourceManualRetry, Result: &store.MaterializedResultSource{Ref: controllerwire.NodeRef{RunID: "previous", NodeID: "job"}, StateSeq: 8}}
	f.snapshot.Run.Retry = &store.RetrySelection{Mode: "failed", Results: map[string]store.MaterializedResultSource{"job": *sourceResult.Result}}
	setTestPolicies(&h, func(_ context.Context, layer testPolicyName, _ EvaluationInput, _ store.NodeRow, _ StorageBinding) (ownerLayerResult, error) {
		if layer.Name == "admission" && kind == store.Materialize {
			t.Fatal("copied result entered compute admission")
		}
		return ownerLayerResult{}, nil
	})
	dispatched, err := releasedRequest(&h, t.Context(), input, *input.Accepted, &source.Entry{}, row, store.DecisionReady)
	if err != nil || dispatched != nil || !probe.request.MaterializeOnly || probe.request.Policy != store.SourceManualRetry || !reflect.DeepEqual(probe.request.Source, sourceResult) {
		t.Fatalf("probe = %+v, dispatched = %+v, error %v", probe.request, dispatched, err)
	}
	want := []string(nil)
	if kind == store.Dispatch {
		want = []string{"ready"}
	}
	if !reflect.DeepEqual(f.trace, want) {
		t.Fatalf("transitions = %v, want %v", f.trace, want)
	}
}
