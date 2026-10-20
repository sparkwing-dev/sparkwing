package store

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func checkStoreGCFences(t *testing.T, op *Operator, lease SingletonLease) {
	t.Helper()
	wrongHolder, wrongGeneration, wrongWorker := lease, lease, lease
	wrongHolder.Holder += "-other"
	wrongGeneration.Generation++
	wrongWorker.Name = "completion"
	for _, invalid := range []SingletonLease{{}, wrongHolder, wrongGeneration, wrongWorker} {
		refuseStoreGCCollection(t, op, invalid)
	}
}

func refuseStoreGCCollection(t *testing.T, op *Operator, lease SingletonLease) {
	t.Helper()
	calls := 0
	deleter := blobDeleteFunc(func(context.Context, blob.Blob) error {
		calls++
		return errors.New("unauthorized physical deletion")
	})
	n, err := op.CollectBlobTombstones(t.Context(), lease, 100, deleter)
	if n != 0 || !errors.Is(err, ErrLockHeld) || calls != 0 {
		t.Fatalf("invalid collection fence removed=%d calls=%d: %v", n, calls, err)
	}
}
