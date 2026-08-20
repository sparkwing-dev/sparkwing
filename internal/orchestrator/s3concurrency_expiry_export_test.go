package orchestrator

import (
	"context"
	"fmt"
	"time"
)

// ExpireS3ConcurrencyHolderForTest moves one live holder behind the
// persisted lease boundary without waiting for wall time.
func ExpireS3ConcurrencyHolderForTest(ctx context.Context, backend ConcurrencyBackend, key, holderID string) error {
	c, ok := backend.(*s3Concurrency)
	if !ok {
		return fmt.Errorf("concurrency backend is %T, want S3", backend)
	}
	return c.mutate(ctx, key, func(doc *s3SlotDoc, exists bool, now time.Time) (bool, error) {
		if !exists {
			return false, fmt.Errorf("concurrency key %q does not exist", key)
		}
		holder := findHolder(doc, holderID)
		if holder == nil {
			return false, fmt.Errorf("holder %q does not exist for key %q", holderID, key)
		}
		if holder.Superseded || holder.LeaseExpiresNS <= now.UnixNano() {
			return false, fmt.Errorf("holder %q is not live", holderID)
		}
		holder.LeaseExpiresNS = now.Add(-time.Second).UnixNano()
		return true, nil
	})
}
