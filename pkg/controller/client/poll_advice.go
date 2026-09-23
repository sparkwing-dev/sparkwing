package client

import (
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// MaxPollAdvice caps how far one suggestion can widen a claim loop's cadence.
// A runner that stops polling for longer than a controller's placement hold
// drops out of local-first placement, so the ceiling sits below the shipped
// hold even after the runner adds its own spread.
const MaxPollAdvice = 8 * time.Second

// PollAdvice reports the interval the controller most recently suggested a
// claim loop wait before polling again, or zero when it suggested none. The
// suggestion is advice, not a limit: a caller widens its own cadence to it and
// never shortens below what it was configured with.
func (c *Client) PollAdvice() time.Duration {
	return time.Duration(c.pollAdvice.Load())
}

// safety: a response carrying no suggestion clears the last one, because a
// controller with work to hand out wants its fleet back at full cadence.
func (c *Client) recordPollAdvice(resp *http.Response) {
	c.pollAdvice.Store(int64(pollAdviceOf(resp)))
}

// WithRunnerIdentity names the runner this client speaks for: a value stable
// for the life of the process, such as a pool runner's holder prefix or an
// enrolled agent's name. The controller budgets claims and heartbeats per
// runner, so a fleet sharing one token sets distinct identities and a single
// runner keeps one across its whole run. A per-request value would hand the
// runner a fresh budget on every poll and grow the controller's bucket table
// at the fleet's poll rate. It returns the same client for chaining, and is
// safe to call after the client is already serving requests.
func (c *Client) WithRunnerIdentity(id string) *Client {
	c.runnerIdentity.Store(&id)
	return c
}

// WithTriggerNodeRunner names how this client runs the nodes of the triggers
// it claims: "inprocess", "k8s" or "warm". The controller refuses a metered
// credential's trigger claim unless it names k8s or warm, because only those
// run every node under a node claim that credits pay for. It returns the same
// client for chaining; set it before the client serves requests.
func (c *Client) WithTriggerNodeRunner(kind string) *Client {
	c.triggerNodeRunner = kind
	return c
}

// WithAllowRepos sends patterns, the repository list this runner's owner
// allows, with every trigger and node claim, so the controller hands this
// client only work from those repositories. An empty non-nil list claims
// nothing; nil sends no list. A controller older than the field refuses the
// claim with 400.
func (c *Client) WithAllowRepos(patterns []string) *Client {
	if patterns == nil {
		c.allowRepos = nil
		return c
	}
	c.allowRepos = append([]string{}, patterns...)
	return c
}

// meteredInProcessNodesCode is the code the controller answers a metered
// trigger claim with when it names no node runner that claims each node.
const meteredInProcessNodesCode = "metered_inprocess_nodes"

// RunnerIdentity reports the identity this client sends, or the empty string
// when it sends none.
func (c *Client) RunnerIdentity() string {
	if id := c.runnerIdentity.Load(); id != nil {
		return *id
	}
	return ""
}

func (c *Client) setRunnerIdentity(req *http.Request) {
	if id := c.RunnerIdentity(); id != "" {
		req.Header.Set(store.RunnerIdentityHeader, id)
	}
}

func pollAdviceOf(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	raw := resp.Header.Get(store.ClaimPollAfterHeader)
	if raw == "" {
		return 0
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 0
	}
	return min(time.Duration(seconds)*time.Second, MaxPollAdvice)
}
