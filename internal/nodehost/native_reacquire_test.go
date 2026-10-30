package nodehost

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type nativeReacquireStore struct {
	*store.Tenant
	waiting func(context.Context, store.ReadyNode) error
}

func (s nativeReacquireStore) TransitionNode(ctx context.Context, change store.NodeCommand) (runstate.Applied, error) {
	applied, err := s.Tenant.TransitionNode(ctx, change)
	if ready, ok := change.(store.ReadyNode); err == nil && ok && applied.Waiting {
		err = s.waiting(ctx, ready)
	}
	return applied, err
}

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
			var queued atomic.Bool
			config.Store = nativeReacquireStore{Tenant: first.Tenant, waiting: func(ctx context.Context, ready store.ReadyNode) error {
				if queued.Swap(true) {
					t.Error("next job queued again after releasing its competitor")
				}
				state, err := first.Tenant.GetConcurrencyState(ctx, "plan:global:deploys")
				if err != nil {
					return err
				}
				planQueued := false
				for _, waiter := range state.Waiters {
					planQueued = planQueued || waiter.RunID == first.Input.RunID && waiter.NodeID == ""
				}
				if !planQueued {
					t.Error("next job waited without queuing for its plan group")
				}
				row, err := first.Tenant.ReadNode(ctx, runspec.NodeRef{RunID: runspec.RunID(first.Accepted.RunID), NodeID: "first"})
				if err != nil {
					return err
				}
				if row.Outcome != runstate.NodeSuccess {
					t.Errorf("released job did not finish before reacquisition: %+v", row)
				}
				row, err = first.Tenant.ReadNode(ctx, runspec.NodeRef{RunID: runspec.RunID(first.Accepted.RunID), NodeID: "next"})
				if err != nil {
					return err
				}
				if row.ReadyAt != nil || row.ClaimedBy != "" || ready.Ref.NodeID != "next" {
					t.Errorf("next job released before reacquisition: node=%+v binding=%+v", row, ready.Ref)
				}
				if _, err := other.Tenant.TransitionNode(ctx, store.CancelNode{Ref: runspec.NodeRef{RunID: runspec.RunID(other.Accepted.RunID), NodeID: "hold"}, Actor: other.Actor}); err != nil {
					return err
				}
				_, err = other.Tenant.FinishRunAndUnit(ctx, runstate.FinishRunRequest{Unit: other.Actor.Owner, Transition: runstate.RunCompletion{RunID: runspec.RunID(other.Accepted.RunID), Actor: other.Actor, Cause: runstate.RunFinish}})
				return err
			}}
			owner, _, _, err := NewNativeHost(config)
			if err != nil {
				t.Fatal(err)
			}
			owner.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
				if input.NodeID == "first" {
					if _, err := teststore.SQL(t, first.Store).ExecContext(ctx, `UPDATE concurrency_holders SET lease_expires_at=1 WHERE team=? AND run_id=? AND node_id=''`, first.Tenant.Team(), first.Accepted.RunID); err != nil {
						return err
					}
					result, err := other.Tenant.MayStart(ctx, runstate.CapacityBinding{RunID: other.Accepted.RunID, Generation: int64(other.Actor.Owner.Generation)}, other.Actor)
					if err != nil || result.Kind != runstate.AcquireGranted {
						return fmt.Errorf("competitor admission: %+v %w", result, err)
					}
				} else if !queued.Load() {
					t.Error("next job bypassed plan reacquisition")
				}
				return first.report(ctx, input, runstate.NodeSuccess)
			})
			completion, err := owner.Run(t.Context(), first.Input)
			if err != nil || completion.Outcome != "success" || !queued.Load() {
				t.Fatalf("completion=%+v queued=%t error=%v", completion, queued.Load(), err)
			}
		})
	}
}
