package launcher

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// Prepare returns the earliest committed deadline of the acknowledged unexchanged awards.
func (c *ControllerControl) Prepare(ctx context.Context, fences []capability.LeaseRef) (time.Time, error) {
	response, err := c.client.SyncLauncher(ctx, controllerwire.LauncherSyncRequest{Preparing: slices.Clone(fences)})
	if err != nil {
		return time.Time{}, err
	}
	if len(response.Awards)+len(response.Retired) != 0 {
		return time.Time{}, errors.New("preparation returned unobserved Cloud decisions")
	}
	remaining := make(map[capability.LeaseRef]bool, len(fences))
	for _, fence := range fences {
		if remaining[fence] {
			return time.Time{}, errors.New("duplicate preparation fence")
		}
		remaining[fence] = true
	}
	var expires time.Time
	for _, preparation := range response.Prepared {
		if !remaining[preparation.LeaseRef] || preparation.ExpiresAt.IsZero() {
			return time.Time{}, errors.New("controller acknowledged an unexpected preparation fence or missing lease deadline")
		}
		delete(remaining, preparation.LeaseRef)
		if expires.IsZero() || preparation.ExpiresAt.Before(expires) {
			expires = preparation.ExpiresAt
		}
	}
	if len(remaining) > 0 || expires.IsZero() {
		return time.Time{}, &apierr.Error{Code: apierr.LockHeld, Message: "Cloud preparation award ended or was exchanged"}
	}
	return expires, nil
}

func (l *Launcher) keepPreparation(ctx context.Context, fence capability.LeaseRef) (context.Context, func(), error) {
	expires, err := l.Control.Prepare(ctx, []capability.LeaseRef{fence})
	if err != nil {
		return nil, nil, err
	}
	if !expires.After(time.Now()) {
		return nil, nil, lease.ErrLeaseLost
	}
	initial, observed := true, false
	ready := make(chan struct{})
	keeper := lease.Keeper{Interval: limits.Renew(limits.DefaultLease), Lease: limits.DefaultLease, Renew: func(ctx context.Context) (lease.Beat, error) {
		if initial {
			initial = false
			return lease.Beat{ExpiresAt: expires}, nil
		}
		if err := l.Sync(ctx); err != nil {
			return lease.Beat{}, err
		}
		expiry, err := l.Control.Prepare(ctx, []capability.LeaseRef{fence})
		return lease.Beat{ExpiresAt: expiry}, err
	}, Observe: func(lease.Beat) {
		if !observed {
			observed = true
			close(ready)
		}
	}}
	unit, stop := keeper.Run(ctx)
	select {
	case <-unit.Done():
		stop()
		return nil, nil, context.Cause(unit)
	case <-ready:
		return unit, stop, nil
	}
}
