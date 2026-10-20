package lease_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
)

func FuzzKeeperCancellationReason(f *testing.F) {
	f.Add("operator requested stop")
	f.Add("")
	f.Add("cancelled by a newer run\nwith unicode: 雪")
	f.Fuzz(func(t *testing.T, reason string) {
		synctest.Test(t, func(t *testing.T) {
			keeper := lease.Keeper{Interval: 10 * time.Second, Lease: time.Minute, Renew: func(context.Context) (lease.Beat, error) {
				return lease.Beat{CancelRequested: true, CancelReason: reason}, nil
			}}
			ctx, stop := keeper.Run(t.Context())
			defer stop()
			<-ctx.Done()
			var cancelled *lease.CancelError
			want := reason
			if want == "" {
				want = "cancel_requested"
			}
			if !errors.As(context.Cause(ctx), &cancelled) || cancelled.Reason != want {
				t.Fatalf("cancellation reason changed: %v", context.Cause(ctx))
			}
		})
	})
}

func TestKeeperHonorsTheReturnedBoundAcrossSlowAndLostRenewals(t *testing.T) {
	for _, responseDelay := range []time.Duration{0, 5 * time.Second, 25 * time.Second} {
		t.Run(responseDelay.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				bound := start.Add(20 * time.Second)
				calls, observed := 0, 0
				keeper := lease.Keeper{
					Interval: time.Minute, Lease: time.Hour,
					Renew: func(ctx context.Context) (lease.Beat, error) {
						calls++
						if calls == 1 {
							time.Sleep(responseDelay)
							return lease.Beat{ExpiresAt: bound}, nil
						}
						<-ctx.Done()
						return lease.Beat{}, context.Cause(ctx)
					},
					Observe: func(lease.Beat) { observed++ },
				}
				ctx, stop := keeper.Run(t.Context())
				defer stop()
				<-ctx.Done()
				if got := time.Since(start); got != max(20*time.Second, responseDelay) {
					t.Fatalf("keeper stopped after %v, past its returned bound", got)
				}
				if responseDelay > 20*time.Second && observed != 0 {
					t.Fatalf("expired renewal delivered %d successful observations", observed)
				}
				if responseDelay < 20*time.Second && calls != 2 {
					t.Fatalf("keeper made %d renewals; want a renewal within the funded slice", calls)
				}
				if !errors.Is(context.Cause(ctx), lease.ErrSilent) {
					t.Fatalf("cause=%v", context.Cause(ctx))
				}
			})
		})
	}
}
