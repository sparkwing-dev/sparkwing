package launcher

import (
	"context"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// Prepare keeps only acknowledged unexchanged awards alive through launcher preparation.
func (c *ControllerControl) Prepare(ctx context.Context, fences []capability.LeaseRef) error {
	response, err := c.client.SyncLauncher(ctx, controllerwire.LauncherSyncRequest{Preparing: slices.Clone(fences)})
	if err != nil {
		return err
	}
	if len(response.Awards)+len(response.Remove)+len(response.RemoveReservations) != 0 {
		return errors.New("preparation returned unobserved Cloud decisions")
	}
	remaining := make(map[capability.LeaseRef]bool, len(fences))
	for _, fence := range fences {
		if remaining[fence] {
			return errors.New("duplicate preparation fence")
		}
		remaining[fence] = true
	}
	for _, fence := range response.Prepared {
		if !remaining[fence] {
			return errors.New("controller acknowledged an unexpected preparation fence")
		}
		delete(remaining, fence)
	}
	if len(remaining) > 0 {
		return &apierr.Error{Code: apierr.LockHeld, Message: "Cloud preparation award ended or was exchanged"}
	}
	return nil
}

func (l *Launcher) keepPreparation(ctx context.Context, fence capability.LeaseRef) (context.Context, func(), error) {
	if err := l.Control.Prepare(ctx, []capability.LeaseRef{fence}); err != nil {
		return nil, nil, err
	}
	initial := true
	keeper := lease.Keeper{Interval: limits.Renew(limits.DefaultLease), Lease: limits.DefaultLease, Renew: func(ctx context.Context) (lease.Beat, error) {
		if initial {
			initial = false
			return lease.Beat{}, nil
		}
		if err := l.Control.Prepare(ctx, []capability.LeaseRef{fence}); err != nil {
			return lease.Beat{}, err
		}
		return lease.Beat{}, l.Sync(ctx)
	}}
	unit, stop := keeper.Run(ctx)
	return unit, stop, nil
}
