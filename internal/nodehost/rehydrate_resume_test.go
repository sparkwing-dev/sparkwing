package nodehost

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestARetryNeverCopiesOverARowItsRunAlreadyHolds(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "claimed", true: "terminal"}[terminal], func(t *testing.T) {
			f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
			releaseRemote(t, f, "job")
			_, input := claimRemote(t, f, "job")
			if terminal {
				if err := f.report(t.Context(), input, runstate.NodeFailed); err != nil {
					t.Fatal(err)
				}
			}
			ref := runspec.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "job"}
			before, err := f.Tenant.ReadNode(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			result, err := f.Tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: f.Actor, Policy: runstate.SourceManualRetry, MaterializeOnly: true, Source: &runstate.MaterializeSource{Kind: runstate.SourceManualRetry, Result: &runstate.MaterializedResultSource{Ref: ref, StateSeq: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			want := runstate.WaitHolder
			if terminal {
				want = runstate.KeepTerminal
			}
			if result.Kind != want || !reflect.DeepEqual(result.Node, before) {
				t.Fatalf("retry probe = %+v, want unchanged %+v", result, before)
			}
		})
	}
}
