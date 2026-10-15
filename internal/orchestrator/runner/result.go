package runner

import (
	"context"
)

// OutputReader reads a finished node's output bytes.
type OutputReader interface {
	GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error)
}
