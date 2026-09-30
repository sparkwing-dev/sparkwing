package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A claimed node whose cache hit the controller answers as not found runs as
// on a miss; a refused read, a node that is not claimed, and a rerun that
// already skipped the cache read do not.
func TestClaimCacheMiss_RunsTheNodeOnlyForAClaimsVanishedEntry(t *testing.T) {
	ctx := context.Background()
	claimed := NewNodeExecutor(Backends{State: &claimState{}})
	notFound := fmt.Errorf("controller 404: %w", store.ErrNotFound)
	for _, c := range []struct {
		name string
		r    *NodeExecutor
		ctx  context.Context
		err  error
		want bool
	}{
		{"a claim's vanished entry", claimed, ctx, notFound, true},
		{"a claim's refused read", claimed, ctx, errors.New("controller 403: input_undeclared"), false},
		{"a claim's read that worked", claimed, ctx, nil, false},
		{"a rerun that skipped the cache", claimed, withNoCache(ctx), notFound, false},
		{"a node that is not claimed", NewNodeExecutor(Backends{}), ctx, notFound, false},
	} {
		if got := c.r.claimCacheMiss(c.ctx, c.err); got != c.want {
			t.Errorf("%s: claimCacheMiss = %v, want %v", c.name, got, c.want)
		}
	}
}
