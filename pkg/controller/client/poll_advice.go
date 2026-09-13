package client

import (
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// MaxPollAdvice caps how far one suggestion can widen a claim loop's cadence,
// so a controller naming an hour does not park a runner for one.
const MaxPollAdvice = 60 * time.Second

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
