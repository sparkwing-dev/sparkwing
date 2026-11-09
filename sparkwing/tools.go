package sparkwing

import (
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/labels"
)

// NeedsTools restricts the job to agents that have every named tool on their
// PATH. Each name adds the selector term tool:<name>, the label an agent
// advertises for each tool it detects at startup, so NeedsTools("terraform")
// is Requires("tool:terraform") that survives a later Requires call. Names
// are relayed as selectors so new host-detected tools need no SDK release.
// A node no agent can claim waits and marks its run as needing
// attention until one can, for up to its plan's ClaimWait.
func (n *JobNode) NeedsTools(names ...string) *JobNode {
	for _, name := range names {
		if label := labels.ToolPrefix + name; !slices.Contains(n.tools, label) {
			n.tools = append(n.tools, label)
		}
	}
	return n
}

// ClaimWait declares how long each ready job may wait for a runner claim.
// The host requires a positive duration within the operator ceiling when it
// accepts the plan. Leaving ClaimWait unset uses the host's default.
func (p *Plan) ClaimWait(d time.Duration) *Plan {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.claimWait = d
	p.claimWaitSet = true
	return p
}
