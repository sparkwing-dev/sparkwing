package store

import (
	"context"
	"time"
)

// RunBucketMeasureLeased measures once per window under a fenced deployment lease.
func (o *Operator) RunBucketMeasureLeased(ctx context.Context, holder string, window, ttl time.Duration, fn func(context.Context) error) (bool, error) {
	return o.runWindowLeased(ctx, "bucket.measure", "bucket.measure", holder, window, ttl, fn)
}
