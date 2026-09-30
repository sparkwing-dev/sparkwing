package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// OutputReader reads a finished node's output bytes.
type OutputReader interface {
	GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error)
}

func NodeTerminal(n *store.Node) bool {
	return n != nil && n.Status == "done" && n.Outcome != ""
}

// ResultFromNode rebuilds a node's result from its row, reading its output
// through outputs when the row names one. An output that cannot be read is
// left out, and a dependent that needs it reads it again itself.
func ResultFromNode(ctx context.Context, n *store.Node, outputs OutputReader) Result {
	res := Result{Outcome: sparkwing.Outcome(n.Outcome)}
	if n.Error != "" {
		res.Err = errors.New(n.Error)
	}
	if n.OutputRef != nil && outputs != nil {
		if data, err := outputs.GetNodeOutput(ctx, n.RunID, n.NodeID); err == nil && len(data) > 0 {
			res.Output = data
		}
	}
	return res
}

// ErrClaimLost marks a [Result] whose dispatcher stopped because the
// controller refuses its token as revoked or expired.
var ErrClaimLost = errors.New("the controller refuses this dispatcher's token as revoked or expired, so it no longer owns the node")

// ClaimLost is the result of a node whose dispatcher's token died. A dead
// token can neither renew a claim nor write a row, so the node is Cancelled
// and its row left to whoever still owns it: the executor holding a live claim
// of its own, or the reaper once the claim lapses.
func ClaimLost(cause error) Result {
	return Result{Outcome: sparkwing.Cancelled, Err: fmt.Errorf("%w: %w", ErrClaimLost, cause)}
}
