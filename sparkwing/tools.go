package sparkwing

import (
	"fmt"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// NeedsTools restricts the job to agents that have every named tool on their
// PATH. Each name adds the selector term tool:<name>, the label an agent
// advertises for each tool it detects at startup, so NeedsTools("terraform")
// is Requires("tool:terraform") that survives a later Requires call. Names
// come from a fixed list (aws, buildx, crane, docker, git, go, golangci-lint,
// helm, kubectl, node, npm, shellcheck, terraform); any other name panics at
// plan time. A node no agent can claim waits and marks its run as needing
// attention until one can, for up to its plan's ClaimWait.
func (n *JobNode) NeedsTools(names ...string) *JobNode {
	for _, name := range names {
		if !match.IsKnownTool(name) {
			panic(fmt.Sprintf("sparkwing: NeedsTools(%q): not a known tool; known tools are %v", name, match.KnownTools))
		}
		if label := match.ToolPrefix + name; !slices.Contains(n.tools, label) {
			n.tools = append(n.tools, label)
		}
	}
	return n
}

// ClaimWait bounds how long each of the run's ready nodes waits for an agent
// to claim it before the controller fails it as unclaimable. Zero or less
// keeps the controller's default of 24 hours.
func (p *Plan) ClaimWait(d time.Duration) *Plan {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.claimWait = max(d, 0)
	return p
}

// ClaimWaitValue returns the wait ClaimWait set, or zero for the default.
func (p *Plan) ClaimWaitValue() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.claimWait
}
