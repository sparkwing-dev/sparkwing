package nodehost

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type delayedCancellationReport struct {
	NodeTransitionStore
	committed bool
	wait      func(context.Context) error
}

func TestOwnerFinishesCancellationAfterACommittedPolicyResult(t *testing.T) {
	state, host, input := newOwnerFixture(t, true)
	state.snapshot.Nodes = []runstate.NodeRow{{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Status: runstate.StatusPending, Spec: marshalEventFixture(t, nodeproto.JobSpec{ID: "job"})}}
	host.Decide = store.Decide
	project := host.Project
	host.Project = func(snapshot store.RunSnapshot) (store.ScheduleSnapshot, error) {
		graph, err := project(snapshot)
		graph.CancelRequested = snapshot.Run.CancelRequestedAt != nil
		return graph, err
	}
	setTestPolicies(&host, func(ctx context.Context, _ testPolicyName, _ EvaluationInput, _ runstate.NodeRow, _ StorageBinding) (ownerLayerResult, error) {
		now := time.Now()
		state.mu.Lock()
		state.snapshot.Run.CancelRequestedAt = &now
		state.snapshot.Run.StateSeq++
		state.snapshot.Nodes[0].CancelRequestedAt = &now
		state.snapshot.Nodes[0].StateSeq++
		state.mu.Unlock()
		select {
		case state.changed <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ownerLayerResult{}, errors.Join(context.Cause(ctx), &apierr.Error{Code: apierr.Unauthenticated, Message: "credential is not live"})
	})
	completion, err := host.Run(t.Context(), input)
	if err != nil || completion.Outcome != "cancelled" || state.finished.Transition.RunID != "run" {
		t.Fatalf("committed cancellation left the owner unfinished: %+v, %v", completion, err)
	}
}

func (s *delayedCancellationReport) TransitionNode(ctx context.Context, command store.NodeCommand) (runstate.Applied, error) {
	if captureNodeCommand(command).Cause == runstate.CauseReport {
		if err := s.wait(ctx); err != nil {
			return runstate.Applied{}, err
		}
		s.committed = true
	}
	return s.NodeTransitionStore.TransitionNode(ctx, command)
}

func TestCancellationReportsUseTheLiveClaimLifetime(t *testing.T) {
	for _, setup := range []bool{false, true} {
		for _, expires := range []bool{false, true} {
			name := map[bool]string{false: "executor", true: "setup"}[setup] + "/" + map[bool]string{false: "live", true: "expired"}[expires]
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					input, state, deps, _ := executorFixture(t)
					reports := &delayedCancellationReport{NodeTransitionStore: state, wait: func(ctx context.Context) error {
						timer := time.NewTimer(limits.HeartbeatTimeout + time.Second)
						defer timer.Stop()
						select {
						case <-timer.C:
							return nil
						case <-ctx.Done():
							return context.Cause(ctx)
						}
					}}
					deps.Store = reports
					keeper := lease.Keeper{Interval: time.Second, Lease: time.Minute, Renew: func(context.Context) (lease.Beat, error) {
						beat := lease.Beat{CancelRequested: true}
						if expires {
							beat.ExpiresAt = time.Now().Add(time.Second)
						}
						return beat, nil
					}}
					ctx, stop := startHostKeeper(t.Context(), keeper)
					defer stop()
					var err error
					if setup {
						err = claimSetupFailure(ctx, input, &apierr.Error{Code: apierr.Unauthenticated, Message: "credential is not live"}, deps.Masker.Mask, reports.TransitionNode)
					} else {
						host, createErr := NewNodeExecutor(deps)
						if createErr != nil {
							t.Fatal(createErr)
						}
						err = host.Execute(ctx, input)
					}
					if expires {
						if reports.committed || !errors.Is(err, lease.ErrSilent) {
							t.Fatalf("report ignored claim expiry: committed=%v error=%v", reports.committed, err)
						}
						return
					}
					if !reports.committed || len(state.changes) != 1 || state.changes[0].To != runstate.NodeCancelled {
						t.Fatalf("live cancellation report did not commit: changes=%+v error=%v", state.changes, err)
					}
				})
			})
		}
	}
}
