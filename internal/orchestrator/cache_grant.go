package orchestrator

import (
	"context"
	"errors"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

// RequestRunCacheGrant returns the grant a claimed run's cache traffic
// carries: source fetches, the binary cache, artifacts and the SDK's caches.
// It asks the controller at controllerURL with the executor's own token, so
// every executor that touches the cache gets the grant the same way and no
// executor holds the cache's operator token. It returns "" when the controller
// mints none; the run then goes without the cache rather than failing.
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
