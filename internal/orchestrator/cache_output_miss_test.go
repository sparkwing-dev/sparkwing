package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A cache hit whose entry or bytes are gone, or whose bytes fail their
// digest, runs as on a miss, on a claim and on a laptop alike; a refused
// read and a rerun that already skipped the cache read do not.
func TestCacheOutputMiss_RerunsAHitWithoutGoodBytes(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"a vanished entry", ctx, fmt.Errorf("controller 404: %w", store.ErrNotFound), true},
		{"bytes that fail their digest", ctx, fmt.Errorf("download output: %w", store.ErrOutputCorrupt), true},
		{"a refused read", ctx, errors.New("controller 403: input_undeclared"), false},
		{"a read that worked", ctx, nil, false},
		{"a rerun that skipped the cache", withNoCache(ctx), store.ErrOutputCorrupt, false},
	} {
		if got := cacheOutputMiss(c.ctx, c.err); got != c.want {
			t.Errorf("%s: cacheOutputMiss = %v, want %v", c.name, got, c.want)
		}
	}
}
