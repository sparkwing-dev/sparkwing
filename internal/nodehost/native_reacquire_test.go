package nodehost

import (
	"context"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestNativeOwnerReacquiresPlanBeforeNextRelease(t *testing.T) {
	for _, compute := range []bool{false, true} {
		t.Run(fmt.Sprintf("compute=%t", compute), func(t *testing.T) {
			plan := planGrouped("global", "fail", "first", "next")
			plan.Jobs[1].Needs = []string{"first"}
			first := newStoreHostFixture(t, plan)
			other := newStoreHostFixtureOn(t, first.Store, planGrouped("global", "fail", "hold"), runspec.Options{})
			config, _, _, _ := nativeHostFixture(t)
			config.Store, config.Owner = first.Tenant, ownerOperationsFixture(first.Host)
			var machine *LocalAdmission
			if compute {
				machine = &LocalAdmission{Daemon: newFakeDaemon()}
			}
			config.Admission = ComposeNativeAdmission(first.Tenant, machine, false)
			queued := false
			config.Admission.capacity.Queued = func(ctx context.Context, ref controllerwire.NodeRef, _ store.AcquireSlotResponse) error {
				queued = true
				row, err := first.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: first.Accepted.RunID, NodeID: "first"})
				if err != nil {
					return err
				}
				if row.Outcome != store.NodeSuccess {
					t.Errorf("released job did not finish before reacquisition: %+v", row)
				}
				row, err = first.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: first.Accepted.RunID, NodeID: "next"})
				if err != nil {
					return err
				}
				if row.ReadyAt != nil || row.ClaimedBy != "" || ref.NodeID != "" {
					t.Errorf("next job released before reacquisition: node=%+v binding=%+v", row, ref)
				}
				if _, err := other.Tenant.TransitionNode(ctx, store.NodeTransition{Ref: controllerwire.NodeRef{RunID: other.Accepted.RunID, NodeID: "hold"}, To: store.NodeCancelled, Actor: other.Actor, Cause: store.CauseCancel}); err != nil {
					return err
				}
				_, err = other.Tenant.FinishRunAndUnit(ctx, store.FinishRunRequest{Unit: other.Actor.Owner, Transition: store.RunTransition{RunID: other.Accepted.RunID, Actor: other.Actor, To: store.RunFailed, Cause: store.RunFinish}})
				return err
			}
			owner, _, _, err := NewNativeHost(config)
			if err != nil {
				t.Fatal(err)
			}
			owner.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
				if input.NodeID == "first" {
					if _, err := teststore.SQL(t, first.Store).ExecContext(ctx, `UPDATE concurrency_holders SET lease_expires_at=1 WHERE team=? AND run_id=? AND node_id=''`, first.Tenant.Team(), first.Accepted.RunID); err != nil {
						return err
					}
					result, err := other.Tenant.AdmitCapacity(ctx, store.CapacityBinding{RunID: other.Accepted.RunID, Generation: other.Actor.Owner.Generation}, other.Actor)
					if err != nil || result.Kind != store.AcquireGranted {
						return fmt.Errorf("competitor admission: %+v %w", result, err)
					}
				} else if !queued {
					t.Error("next job bypassed plan reacquisition")
				}
				return first.report(ctx, input, store.NodeSuccess)
			})
			completion, err := owner.Run(t.Context(), first.Input)
			if err != nil || completion.Outcome != "success" || !queued {
				t.Fatalf("completion=%+v queued=%t error=%v", completion, queued, err)
			}
		})
	}
}
