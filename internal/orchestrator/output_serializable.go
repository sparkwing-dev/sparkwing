package orchestrator

import (
	"fmt"
)

const processBoundaryGuide = "docs/migrations/v0.36.0.md#process-per-node"

func nodeOutputMarshalError(nodeID string, output any, err error) error {
	return fmt.Errorf(
		"%s: output of type %T could not be encoded as JSON: %w\n"+
			"  a node's output reaches downstream nodes only by JSON round-trip through the run store\n"+
			"  see %s",
		nodeID, output, err, processBoundaryGuide)
}
