package nodehost

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestARetryNeverCopiesOverARowItsRunAlreadyHolds(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "claimed", true: "terminal"}[terminal], func(t *testing.T) {
			f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
			releaseRemote(t, f, "job")
			_, input := claimRemote(t, f, "job")
			if terminal {
				if err := f.report(t.Context(), input, store.NodeFailed); err != nil {
					t.Fatal(err)
				}
			}
			ref := controllerwire.NodeRef{RunID: f.Input.RunID, NodeID: "job"}
			before, err := f.Tenant.ReadNode(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			result, err := f.Tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: f.Actor, Policy: store.SourceManualRetry, MaterializeOnly: true, Source: &store.MaterializeSource{Kind: store.SourceManualRetry, Result: &store.MaterializedResultSource{Ref: ref, StateSeq: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			want := store.WaitHolder
			if terminal {
				want = store.KeepTerminal
			}
			if result.Kind != want || !reflect.DeepEqual(result.Node, before) {
				t.Fatalf("retry probe = %+v, want unchanged %+v", result, before)
			}
		})
	}
}
