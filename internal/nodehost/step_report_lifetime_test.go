package nodehost

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestStepCompletionUsesTheLiveClaimLifetime(t *testing.T) {
	for _, sink := range []string{"state", "diagnostic"} {
		for _, expires := range []bool{false, true} {
			name := sink + "/" + map[bool]string{false: "live", true: "expired"}[expires]
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					input, state, deps, _ := executorFixture(t)
					committed := false
					wait := func(ctx context.Context) error {
						timer := time.NewTimer(limits.HeartbeatTimeout + time.Second)
						defer timer.Stop()
						select {
						case <-timer.C:
							committed = true
							return nil
						case <-ctx.Done():
							return context.Cause(ctx)
						}
					}
					deps.Store = &executorStepStore{NodeTransitionStore: state, write: func(ctx context.Context, report store.StepReport) error {
						if sink == "state" && report.State == "passed" {
							return wait(ctx)
						}
						return nil
					}}
					deps.Log = func(ctx context.Context, _ NodeInput, frame nodewire.FrameLog) error {
						if sink == "diagnostic" && frame.Event == "step_end" {
							return wait(ctx)
						}
						return nil
					}
					keeper := lease.Keeper{Interval: time.Second, Lease: time.Minute, Renew: func(context.Context) (lease.Beat, error) {
						beat := lease.Beat{}
						if expires {
							beat.ExpiresAt = time.Now().Add(time.Second)
						}
						return beat, nil
					}}
					ctx, stop := startHostKeeper(t.Context(), keeper)
					defer stop()
					host, err := NewNodeExecutor(deps)
					if err != nil {
						t.Fatal(err)
					}
					started := time.Now()
					err = host.Execute(ctx, input)
					if expires {
						if committed || !errors.Is(err, lease.ErrSilent) || time.Since(started) != time.Second {
							t.Fatalf("step report ignored claim expiry: committed=%v elapsed=%v error=%v", committed, time.Since(started), err)
						}
						return
					}
					if err != nil || !committed || state.changes[len(state.changes)-1].To != runstate.NodeSuccess {
						t.Fatalf("slow step completion failed with a live claim: changes=%+v error=%v", state.changes, err)
					}
				})
			})
		}
	}
}
