package orchestrator

import (
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type runNodeConfig struct {
	claimed       bool
	claimFence    store.NodeClaimFence
	gitcacheURL   string
	gitcacheGrant string
	repoAllowlist *sourceurl.RepoAllowlist
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

// WithGitcache hands a claimed node the cache and the grant its run's cache
// traffic carries, so concurrent nodes of different runs never share one.
func WithGitcache(url, grant string) RunNodeOption {
	return func(c *runNodeConfig) {
		c.gitcacheURL = url
		c.gitcacheGrant = grant
	}
}
