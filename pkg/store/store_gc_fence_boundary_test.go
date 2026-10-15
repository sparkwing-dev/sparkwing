package store_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkStoreGCFences(ctx context.Context, op *store.Operator, lease store.SingletonLease) error {
	wrongHolder, wrongGeneration, wrongWorker := lease, lease, lease
	wrongHolder.Holder += "-other"
	wrongGeneration.Generation++
	wrongWorker.Name = "completion"
	for _, invalid := range []store.SingletonLease{{}, wrongHolder, wrongGeneration, wrongWorker} {
		if err := refuseStoreGCCollection(ctx, op, invalid); err != nil {
			return err
		}
	}
	return nil
}

func refuseStoreGCCollection(ctx context.Context, op *store.Operator, lease store.SingletonLease) error {
	calls := 0
	deleter := storeBoundaryDeleter(func(context.Context, blob.Blob) error {
		calls++
		return errors.New("unauthorized physical deletion")
	})
	n, err := op.CollectBlobTombstones(ctx, lease, 100, deleter)
	if n != 0 || !errors.Is(err, store.ErrLockHeld) || calls != 0 {
		return fmt.Errorf("invalid collection fence removed=%d calls=%d: %v", n, calls, err)
	}
	return nil
}

func checkStoreGCTakeoverBoundary(ctx context.Context, s *store.Store, lease store.SingletonLease) error {
	if _, err := s.DB().ExecContext(ctx, "UPDATE leases SET until=? WHERE name=? AND holder=? AND generation=?", time.Now().Add(-time.Second).UnixNano(), lease.Name, lease.Holder, lease.Generation); err != nil {
		return err
	}
	op := s.AsOperator()
	if err := refuseStoreGCCollection(ctx, op, lease); err != nil {
		return err
	}
	replacement, err := op.AcquireSingletonLease(ctx, "maintenance", lease.Holder, time.Hour)
	if err != nil {
		return err
	}
	if replacement.Generation <= lease.Generation {
		return fmt.Errorf("collection takeover did not advance generation: %+v", replacement)
	}
	if err := refuseStoreGCCollection(ctx, op, lease); err != nil {
		return err
	}
	deleter := storeBoundaryDeleter(func(context.Context, blob.Blob) error {
		return errors.New("empty collection attempted physical deletion")
	})
	if n, err := op.CollectBlobTombstones(ctx, replacement, 100, deleter); n != 0 || err != nil {
		return fmt.Errorf("current collection fence removed=%d: %v", n, err)
	}
	return nil
}
