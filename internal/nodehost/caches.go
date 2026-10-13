package nodehost

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: saves only after a success, so a failed or cancelled install never seeds the cache.
func cachesLayer(cfg *Config, next Executor) Executor {
	return func(ctx context.Context, n *NodeRun) Result {
		if len(n.Plan.Caches) == 0 {
			return next(ctx, n)
		}
		runs := depcache.RestoreAll(ctx, n.Plan.Caches, n.Plan.ID, cfg.Session.Dir)
		res := next(ctx, n)
		depcache.SaveAll(ctx, runs, resultErr(res))
		return res
	}
}

func resultErr(res Result) error {
	switch {
	case res.Outcome == sparkwing.Success:
		return nil
	case res.Err != nil:
		return res.Err
	default:
		return fmt.Errorf("node ended %s", res.Outcome)
	}
}
