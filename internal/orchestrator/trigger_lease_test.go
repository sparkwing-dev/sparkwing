package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/quick"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type scriptedTriggerHeartbeat struct {
	renew func(context.Context, string, time.Duration) (bool, error)
}

func (s scriptedTriggerHeartbeat) HeartbeatTrigger(ctx context.Context, id string, duration time.Duration) (bool, error) {
	return s.renew(ctx, id, duration)
}

func TestClaimedTriggerKeeperPreservesFenceProperty(t *testing.T) {
	property := func(id string, generation uint64, cancelled bool) bool {
		seq := int64(generation>>1) | 1
		trig := &store.Trigger{ID: id, ClaimSeq: seq}
		parent := store.WithTriggerClaimFence(t.Context(), store.TriggerClaimFence{ClaimGeneration: -seq})
		calls, fenced := 0, false
		state := scriptedTriggerHeartbeat{renew: func(ctx context.Context, runID string, duration time.Duration) (bool, error) {
			calls++
			fence, ok := store.TriggerClaimFenceFromContext(ctx)
			fenced = ok && fence.ClaimGeneration == seq && runID == id && duration == time.Minute
			return cancelled, fmt.Errorf("claim refused: %w", store.ErrLockHeld)
		}}
		unit, stop, err := keepClaimedTrigger(parent, state, trig, time.Minute)
		var refusal *apierr.Error
		return calls == 1 && fenced && unit == nil && stop == nil && errors.As(err, &refusal) && refusal.Code == apierr.LockHeld
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestClaimedTriggerKeeperRefusesBeforeDispatch(t *testing.T) {
	for _, refusal := range []error{store.ErrNotFound, store.ErrLockHeld, store.ErrInsufficientCredits, errors.New("store unavailable")} {
		t.Run(refusal.Error(), func(t *testing.T) {
			state := scriptedTriggerHeartbeat{renew: func(context.Context, string, time.Duration) (bool, error) { return false, refusal }}
			unit, stop, err := keepClaimedTrigger(t.Context(), state, &store.Trigger{ID: "run", ClaimSeq: 7}, time.Minute)
			if err == nil || unit != nil || stop != nil {
				t.Fatalf("refused claim started a keeper: unit=%v stop=%v err=%v", unit, stop != nil, err)
			}
		})
	}
	state := scriptedTriggerHeartbeat{renew: func(context.Context, string, time.Duration) (bool, error) { return true, nil }}
	unit, stop, err := keepClaimedTrigger(t.Context(), state, &store.Trigger{ID: "run", ClaimSeq: 7}, time.Minute)
	var cancelled *lease.CancelError
	if !errors.As(err, &cancelled) || unit != nil || stop != nil {
		t.Fatalf("cancelled claim started a keeper: unit=%v stop=%v err=%v", unit, stop != nil, err)
	}
}

func TestClaimedTriggerKeeperCancelsAndJoins(t *testing.T) {
	for _, refusal := range []error{nil, store.ErrNotFound, store.ErrLockHeld, store.ErrInsufficientCredits} {
		t.Run(fmt.Sprint(refusal), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				state := scriptedTriggerHeartbeat{renew: func(context.Context, string, time.Duration) (bool, error) {
					calls++
					if calls == 1 {
						return false, nil
					}
					return refusal == nil, refusal
				}}
				unit, stop, err := keepClaimedTrigger(t.Context(), state, &store.Trigger{ID: "run", ClaimSeq: 9}, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				stop()
				var cancelled *lease.CancelError
				if refusal == nil && !errors.As(context.Cause(unit), &cancelled) || refusal != nil && !errors.Is(context.Cause(unit), lease.ErrLeaseLost) {
					t.Fatalf("refusal=%v cause=%v", refusal, context.Cause(unit))
				}
				if refusal == nil && !canceledByRun(unit.Err()) {
					t.Fatalf("cancellation before dispatch became a failure: %v", unit.Err())
				}
				if calls != 2 {
					t.Fatalf("renewals=%d; stopped keeper did not join", calls)
				}
			})
		})
	}
}
