package nodehost

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

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
			config.Admission = ComposeStartGate(first.Tenant, machine, false)
			queued := false
			onQueued := func(_ context.Context, ref controllerwire.NodeRef, _ store.AcquireSlotResponse) error {
				ctx := t.Context()
				queued = true
				row, err := first.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: runspec.RunID(first.Accepted.RunID), NodeID: "first"})
				if err != nil {
					return err
				}
				if row.Outcome != store.NodeSuccess {
					t.Errorf("released job did not finish before reacquisition: %+v", row)
				}
				row, err = first.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: runspec.RunID(first.Accepted.RunID), NodeID: "next"})
				if err != nil {
					return err
				}
				if row.ReadyAt != nil || row.ClaimedBy != "" || ref.NodeID != "" {
					t.Errorf("next job released before reacquisition: node=%+v binding=%+v", row, ref)
				}
				if _, err := other.Tenant.TransitionNode(ctx, store.CancelNode{Ref: controllerwire.NodeRef{RunID: runspec.RunID(other.Accepted.RunID), NodeID: "hold"}, Actor: other.Actor}); err != nil {
					return err
				}
				_, err = other.Tenant.FinishRunAndUnit(ctx, store.FinishRunRequest{Unit: other.Actor.Owner, Transition: store.RunCompletion{RunID: runspec.RunID(other.Accepted.RunID), Actor: other.Actor, Cause: store.RunFinish}})
				return err
			}
			owner, _, _, err := NewNativeHost(config)
			if err != nil {
				t.Fatal(err)
			}
			wait := owner.Wait
			owner.Wait = func(ctx context.Context, snapshot store.RunSnapshot) error {
				state, err := first.Tenant.GetConcurrencyState(ctx, "plan:global:deploys")
				if errors.Is(err, store.ErrNotFound) {
					return wait(ctx, snapshot)
				}
				if err != nil {
					return err
				}
				if !queued {
					for _, waiter := range state.Waiters {
						if waiter.RunID == first.Input.RunID {
							return onQueued(ctx, controllerwire.NodeRef{RunID: runspec.RunID(first.Input.RunID)}, store.AcquireSlotResponse{Kind: store.AcquireQueued})
						}
					}
				}
				return wait(ctx, snapshot)
			}
			owner.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
				if input.NodeID == "first" {
					if _, err := teststore.SQL(t, first.Store).ExecContext(ctx, `UPDATE concurrency_holders SET lease_expires_at=1 WHERE team=? AND run_id=? AND node_id=''`, first.Tenant.Team(), first.Accepted.RunID); err != nil {
						return err
					}
					result, err := other.Tenant.MayStart(ctx, store.CapacityBinding{RunID: other.Accepted.RunID, Generation: int64(other.Actor.Owner.Generation)}, other.Actor)
					if err != nil || result.Kind != store.AcquireGranted {
						return fmt.Errorf("competitor admission: %+v %w", result, err)
					}
				} else if !queued {
					t.Error("next job bypassed plan reacquisition")
				}
				return first.report(ctx, input, store.NodeSuccess)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			completion, err := owner.Run(ctx, first.Input)
			if err != nil || completion.Outcome != "success" || !queued {
				t.Fatalf("completion=%+v queued=%t error=%v", completion, queued, err)
			}
		})
	}
}
