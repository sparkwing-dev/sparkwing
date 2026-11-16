package lease

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestCancellationObservationRetainsReportingAuthorityWithoutRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		requested, ready := make(chan struct{}), make(chan struct{})
		calls := 0
		keeper := Keeper{
			Lease: 3 * time.Minute, Interval: time.Minute,
			Renew: func(context.Context) (Beat, error) {
				calls++
				return Beat{}, nil
			},
			Observe: func(Beat) { close(ready) },
			WaitCancellation: func(ctx context.Context) *CancelError {
				select {
				case <-ctx.Done():
					return nil
				case <-requested:
					return &CancelError{Reason: "parent cancelled"}
				}
			},
		}
		unit, stop := keeper.Run(t.Context())
		<-ready
		close(requested)
		<-unit.Done()
		var cause *CancelError
		if !errors.As(context.Cause(unit), &cause) || cause.Reason != "parent cancelled" || calls != 1 {
			t.Fatalf("cancellation=%v renewals=%d", context.Cause(unit), calls)
		}
		if ExecutionContext(unit).Err() != nil {
			t.Fatal("requested cancellation revoked cleanup and reporting authority")
		}
		stop()
		if ExecutionContext(unit).Err() == nil {
			t.Fatal("keeper stop left reporting authority alive")
		}
	})
}

func TestKeeperStopJoinsCancellationObserver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		observing, release := make(chan struct{}), make(chan struct{})
		keeper := Keeper{
			Lease: 3 * time.Minute, Interval: time.Minute,
			Renew: func(context.Context) (Beat, error) { return Beat{}, nil },
			WaitCancellation: func(ctx context.Context) *CancelError {
				close(observing)
				<-ctx.Done()
				<-release
				return nil
			},
		}
		_, stop := keeper.Run(t.Context())
		<-observing
		stopped := make(chan struct{})
		go func() { stop(); close(stopped) }()
		synctest.Wait()
		select {
		case <-stopped:
			t.Fatal("keeper stopped while cancellation observer was still alive")
		default:
		}
		close(release)
		<-stopped
	})
}
