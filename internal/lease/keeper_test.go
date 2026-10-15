package lease_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
)

func TestKeeperCauses(t *testing.T) {
	acceptance.CheckKeeperCauses(t)
}

func TestKeeperConflictAndStop(t *testing.T) {
	acceptance.CheckKeeperConflictAndStop(t)
}

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
