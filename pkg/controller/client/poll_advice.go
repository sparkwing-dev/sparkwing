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
const MaxPollAdvice = 15 * time.Second

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

// safety: runners sharing one token must be told apart by the controller's
// per-runner budgets, so every claim and heartbeat names the runner behind it.
func setRunnerIdentity(req *http.Request, id string) {
	if id != "" {
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
