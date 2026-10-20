package lease_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
)

func TestExecutionBoundSurvivesCancellationAndBoundsCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bound := time.Now().Add(3 * time.Second)
		keeper := lease.Keeper{Lease: time.Minute, Interval: time.Second, Renew: func(context.Context) (lease.Beat, error) {
			return lease.Beat{ExpiresAt: bound, CancelRequested: true, CancelReason: "out of credits"}, nil
		}}
		unit, stop := keeper.Run(t.Context())
		defer stop()
		<-unit.Done()
		execution := lease.ExecutionContext(unit)
		if execution.Err() != nil {
			t.Fatal("requested cancellation exhausted the drain budget")
		}
		cleanup, cancel := context.WithTimeout(execution, time.Minute)
		defer cancel()
		<-cleanup.Done()
		if time.Now() != bound || !errors.Is(context.Cause(cleanup), lease.ErrSilent) {
			t.Fatalf("cleanup ended at %v: %v", time.Now(), context.Cause(cleanup))
		}
	})
}

func TestExecutionBoundFollowsRenewals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		keeper := lease.Keeper{Lease: time.Minute, Interval: time.Second, Renew: func(context.Context) (lease.Beat, error) {
			calls++
			if calls > 2 {
				return lease.Beat{}, errors.New("controller disconnected")
			}
			return lease.Beat{ExpiresAt: time.Now().Add(2 * time.Second)}, nil
		}}
		started := time.Now()
		unit, stop := keeper.Run(t.Context())
		defer stop()
		execution := lease.ExecutionContext(unit)
		<-execution.Done()
		if time.Since(started) != 3*time.Second {
			t.Fatalf("execution stopped after %v", time.Since(started))
		}
	})
}
