package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

// safety: a claim reads only its own run, so the controller picks the
// cross-run source (coalesce leader, cache origin, cross-pipeline ref) for
// the calling node and the claim never names another run.
type resolvedOutputReader interface {
	GetResolvedOutput(ctx context.Context, runID, nodeID string, src client.OutputSource) ([]byte, string, error)
}
