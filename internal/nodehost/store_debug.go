package nodehost

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

type ownerDebugStore interface {
	OwnerDebugPause(context.Context, runspec.NodeRef, int64, runstate.Actor, string) (bool, error)
}

type storeDebugBoundary struct {
	store    ownerDebugStore
	follower stateFollower
}

func (d storeDebugBoundary) WaitDebugBoundary(ctx context.Context, b DebugBoundary) error {
	err := d.follower.follow(ctx, string(b.Ref.RunID), func(ctx context.Context) (bool, error) {
		return d.store.OwnerDebugPause(ctx, b.Ref, b.Generation, b.Actor, b.Reason)
	})
	if ctx.Err() != nil {
		_, cleanupErr := d.store.OwnerDebugPause(context.WithoutCancel(ctx), b.Ref, b.Generation, b.Actor, b.Reason)
		err = errors.Join(err, cleanupErr)
	}
	return err
}
