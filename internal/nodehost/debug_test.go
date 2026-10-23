package nodehost

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

type debugWaitFunc func(context.Context, DebugBoundary) error

func (f debugWaitFunc) WaitDebugBoundary(ctx context.Context, boundary DebugBoundary) error {
	return f(ctx, boundary)
}

func debugOptions(reason string) runspec.Options {
	origin := runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "pause"}
	pause := runspec.PauseSelection{}
	pause.Before.Origin, pause.After.Origin, pause.OnFailure.Origin = origin, origin, origin
	switch reason {
	case store.PauseReasonBefore:
		pause.Before.Set, pause.Before.Value = true, []string{"build"}
	case store.PauseReasonAfter:
		pause.After.Set, pause.After.Value = true, []string{"build"}
	case store.PauseReasonOnFailure:
		pause.OnFailure.Set, pause.OnFailure.Value = true, true
	}
	options := runspec.Options{}
	options.Pause.Set, options.Pause.Value, options.Pause.Origin = true, pause, origin
	return options
}

func TestDebugPausesHoldTheNodeUntilReleased(t *testing.T) {
	for _, reason := range []string{store.PauseReasonBefore, store.PauseReasonAfter, store.PauseReasonOnFailure} {
		t.Run(reason, func(t *testing.T) {
			plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
				{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}},
				{ID: "deploy", Needs: []string{"build"}, Optional: true, Steps: []nodeproto.StepSpec{{ID: "body"}}},
			}}
			f := newStoreHostFixtureOn(t, openStore(t), plan, debugOptions(reason))
			var mu sync.Mutex
			var ran []string
			f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
				mu.Lock()
				ran = append(ran, input.NodeID)
				mu.Unlock()
				outcome := runstate.NodeSuccess
				if input.NodeID == "build" && reason == store.PauseReasonOnFailure {
					outcome = runstate.NodeFailed
				}
				return f.report(ctx, input, outcome)
			})
			var paused []string
			f.Host.Debug = debugWaitFunc(func(ctx context.Context, b DebugBoundary) error {
				if b.Ref != (runspec.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "build"}) || b.Reason != reason || b.Actor != f.Actor || b.Timeout != defaultDebugPauseTimeout {
					return errors.New("pause lost its selected policy or owner authority")
				}
				mu.Lock()
				defer mu.Unlock()
				if slices.Contains(ran, "build") != (reason != store.PauseReasonBefore) || slices.Contains(ran, "deploy") {
					t.Errorf("execution at pause: %v", ran)
				}
				row, err := f.Tenant.ReadNode(ctx, runspec.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "deploy"})
				if err != nil || row.ReadyAt != nil || row.ClaimedBy != "" {
					t.Errorf("downstream released during pause: %+v error=%v", row, err)
				}
				paused = append(paused, b.Reason)
				return nil
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			completion, err := f.Host.Run(ctx, f.Input)
			want := "success"
			if reason == store.PauseReasonOnFailure {
				want = "failed"
			}
			if err != nil || completion.Outcome != want || !slices.Equal(paused, []string{reason}) {
				t.Fatalf("completion=%+v pauses=%v error=%v", completion, paused, err)
			}
			if !slices.Contains(ran, "deploy") && want == "success" {
				t.Fatal("downstream never ran after release")
			}
		})
	}
}

func TestDebugPauseUsesSealedTimeoutBeforeExecution(t *testing.T) {
	options := debugOptions(store.PauseReasonBefore)
	options.PauseTimeout.Set, options.PauseTimeout.Value = true, time.Minute
	options.PauseTimeout.Origin = runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "pause_timeout"}
	f := newStoreHostFixtureOn(t, openStore(t), nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}}}}, options)
	var expired bool
	f.Host.Debug = debugWaitFunc(func(_ context.Context, b DebugBoundary) error {
		if b.Timeout != time.Minute {
			t.Errorf("timeout=%v, want sealed minute", b.Timeout)
		}
		expired = true
		return nil
	})
	f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		if !expired {
			t.Error("executed before the pause adapter released")
		}
		return f.report(ctx, input, runstate.NodeSuccess)
	})
	if completion, err := f.Host.Run(t.Context(), f.Input); err != nil || completion.Outcome != "success" || !expired {
		t.Fatalf("completion=%+v expired=%v error=%v", completion, expired, err)
	}
}

func TestDebugCancellationStopsOwnerWithoutChangingTerminalResult(t *testing.T) {
	for _, reason := range []string{store.PauseReasonBefore, store.PauseReasonAfter} {
		t.Run(reason, func(t *testing.T) {
			plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
				{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}},
				{ID: "deploy", Needs: []string{"build"}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
			}}
			f := newStoreHostFixtureOn(t, openStore(t), plan, debugOptions(reason))
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(context.Canceled)
			stopped := errors.New("operator stopped waiting")
			f.Host.Debug = debugWaitFunc(func(context.Context, DebugBoundary) error {
				cancel(stopped)
				return stopped
			})
			f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
				if input.NodeID == "deploy" {
					t.Error("downstream executed during cancelled pause")
				}
				return f.report(ctx, input, runstate.NodeSuccess)
			})
			if _, err := f.Host.Run(ctx, f.Input); !errors.Is(err, stopped) {
				t.Fatalf("error=%v, want cancellation cause", err)
			}
			row, err := f.Tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "build"})
			if err != nil || reason == store.PauseReasonAfter && row.Outcome != runstate.NodeSuccess || reason == store.PauseReasonBefore && row.Status != runstate.StatusPending {
				t.Fatalf("persisted result=%+v error=%v", row, err)
			}
		})
	}
}

func TestDebugRecoveryNodesAndGatesSkipTheirPauses(t *testing.T) {
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "gate", Approval: &nodeproto.ApprovalSpec{Message: "go?"}},
		{ID: "deploy", Needs: []string{"gate"}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "rollback", OnFailureOf: "deploy", Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}}
	options := debugOptions(store.PauseReasonBefore)
	options.Pause.Value.Before.Value = []string{"gate", "rollback"}
	options.Pause.Value.After.Set, options.Pause.Value.After.Value = true, []string{"gate", "rollback"}
	f := newStoreHostFixtureOn(t, openStore(t), plan, options)
	var paused []string
	f.Host.Debug = debugWaitFunc(func(ctx context.Context, b DebugBoundary) error {
		paused = append(paused, string(b.Ref.NodeID)+":"+b.Reason)
		if _, err := f.Tenant.GetApproval(ctx, f.Input.RunID, "gate"); !errors.Is(err, store.ErrNotFound) {
			return errors.New("approval opened before pause-before released")
		}
		return nil
	})
	wait := f.Host.Wait
	f.Host.Wait = func(ctx context.Context, snapshot store.RunSnapshot) error {
		if row, ok := ownerNode(snapshot, "gate"); ok && row.Status == runstate.StatusApprovalPending {
			_, err := f.State.TransitionNode(ctx, store.ResolveApprovalNode{Ref: row.Ref, Actor: f.Control, Outcome: runstate.NodeSuccess, Approval: &runstate.ApprovalResolution{Decision: store.ApprovalResolutionApproved}})
			return err
		}
		return wait(ctx, snapshot)
	}
	f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error { return f.report(ctx, input, runstate.NodeFailed) })
	completion, err := f.Host.Run(t.Context(), f.Input)
	if err != nil || completion.Outcome != "failed" || !slices.Equal(paused, []string{"gate:" + store.PauseReasonBefore}) {
		t.Fatalf("completion=%+v pauses=%v error=%v", completion, paused, err)
	}
}
