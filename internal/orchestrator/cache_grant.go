package orchestrator

import (
	"context"
	"errors"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

// RequestRunCacheGrant authorizes the remaining working-tree bundle transport.
// Callers hold it in memory for that fetch; it never enters a process environment.
func RequestRunCacheGrant(ctx context.Context, controllerURL, token, runID string, logger *slog.Logger) string {
	grant, err := bincache.RequestCacheGrant(ctx, controllerURL, token, runID)
	switch {
	case err == nil:
		return grant
	case errors.Is(err, bincache.ErrNoCacheGrant):
		logger.Debug("controller mints no cache grant; running without the cache", "run_id", runID)
	default:
		logger.Warn("cache grant unavailable; running without the cache", "run_id", runID, "err", err)
	}
	return ""
}
