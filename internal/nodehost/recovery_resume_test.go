package nodehost

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestResumeOnlyAdoptsTerminalRecoveryResults(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "requeued", true: "terminal"}[terminal], func(t *testing.T) {
			f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
			releaseRemote(t, f, "job")
			claim, input := claimRemote(t, f, "job")
			ref := runspec.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "job"}
			waits := 0
			result, err := followReconciliation(t.Context(), f.State, store.ReconcileRequest{Ref: ref, Actor: f.Actor, Policy: runstate.SourceNone}, func(context.Context, runspec.NodeRef, int64) error {
				waits++
				if terminal {
					return f.report(t.Context(), input, runstate.NodeFailed)
				}
				return f.Store.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix)
			})
			if err != nil {
				t.Fatal(err)
			}
			if waits != 1 {
				t.Fatalf("waited %d times", waits)
			}
			if terminal {
				if result.Kind != runstate.KeepTerminal || result.Node.Outcome != runstate.NodeFailed || result.Node.Attempt != 1 || result.Node.Generation != input.Generation {
					t.Fatalf("terminal = %+v", result)
				}
			} else if result.Kind != runstate.Execute || result.Node.Outcome != "" || result.Node.Generation <= input.Generation || result.Node.Attempt != 0 {
				t.Fatalf("requeued = %+v", result)
			}
		})
	}
}
