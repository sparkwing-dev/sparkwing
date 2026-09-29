package runner

import (
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func NodeTerminal(n *store.Node) bool {
	return n != nil && n.Status == "done" && n.Outcome != ""
}

func ResultFromNode(n *store.Node) Result {
	res := Result{Outcome: sparkwing.Outcome(n.Outcome)}
	if n.Error != "" {
		res.Err = errors.New(n.Error)
	}
	if len(n.Output) > 0 {
		res.Output = n.Output
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
