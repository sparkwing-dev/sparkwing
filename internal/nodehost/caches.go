package nodehost

import (
	"context"
	"fmt"
	"log/slog"

	depcacheblob "github.com/sparkwing-dev/sparkwing/internal/blob/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: saves only after a success, so a failed or cancelled install never seeds the cache.
func cachesLayer(cfg *Config, next Executor) Executor {
	return func(ctx context.Context, n *NodeRun) Result {
		if len(n.Plan.Caches) == 0 {
			return next(ctx, n)
		}
		specs := make([]depcache.Spec, len(n.Plan.Caches))
		for i, c := range n.Plan.Caches {
			specs[i] = depcache.Spec(c)
		}
		cacheCtx, err := cacheContext(ctx, cfg, n)
		if err != nil {
			slog.Warn("depcache: storage binding refused; running cold", "node", n.Plan.ID, "err", err)
			return next(ctx, n)
		}
		runs := depcache.RestoreAll(cacheCtx, specs, n.Plan.ID, cfg.Session.Dir)
		res := next(ctx, n)
		if res.Outcome != sparkwing.Success {
			return res
		}
		cacheCtx, err = cacheContext(ctx, cfg, n)
		if err != nil {
			slog.Warn("depcache: storage binding refused; cache not saved", "node", n.Plan.ID, "err", err)
			return res
		}
		depcache.SaveAll(cacheCtx, runs, nil)
		return res
	}
}

func cacheContext(ctx context.Context, cfg *Config, n *NodeRun) (context.Context, error) {
	if cfg.Artifacts == nil {
		return ctx, nil
	}
	data, err := cfg.Artifacts(ctx, n)
	if err != nil {
		return nil, err
	}
	backend := depcacheblob.New(depcacheblob.Data{
		Bytes: data.Bytes, Records: data.Records, Authority: data.Authority,
		Identity: data.Identity, Budget: data.Budget,
	})
	return depcache.WithBackend(ctx, backend), nil
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
