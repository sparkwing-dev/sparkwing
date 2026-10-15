package orchestrator

import (
	"context"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type runNodeConfig struct {
	claimed       bool
	claimFence    store.NodeClaimFence
	repoAllowlist *sourceurl.RepoAllowlist
	hostRemote    remoteNodeHost
}

type remoteNodeHost func(ctx context.Context, trigger *store.Trigger, run *store.Run,
	controllerURL, runID, nodeID, token string,
	allow *sourceurl.RepoAllowlist, logger *slog.Logger) (runner.Result, error)

// hack: tests stand a prebuilt pipeline in for the source fetch and build through this option.
func withRemoteNodeHost(host remoteNodeHost) RunNodeOption {
	return func(c *runNodeConfig) { c.hostRemote = host }
}

type RunNodeOption func(*runNodeConfig)

// ClaimedNodeFence runs the node under a claim another process already holds,
// fencing every controller write and log append with it. The caller is the
// isolated execution, so unlike [ClaimedNode] it hands nothing to a child: a
// Kubernetes Job's pod is the case this serves.
func ClaimedNodeFence(fence store.NodeClaimFence) RunNodeOption {
	return func(c *runNodeConfig) { c.claimFence = fence }
}

func ClaimedNodeAttempt(node *store.Node) RunNodeOption {
	return func(c *runNodeConfig) {
		c.claimed = true
		c.claimFence = store.NodeClaimFence{
			MembershipID: node.ClaimMembershipID, ReservationID: node.ReservationID,
			ClaimGeneration: node.ClaimGeneration,
		}
	}
}

// WithRepoAllowlist holds a claimed node to the repositories this machine's
// owner allowed: a node whose run names any other is refused before its source
// is fetched. An executor started without it -- a Job its run's trigger runner
// created -- builds only the repository that runner already admitted.
func WithRepoAllowlist(allow sourceurl.RepoAllowlist) RunNodeOption {
	return func(c *runNodeConfig) { c.repoAllowlist = &allow }
}
