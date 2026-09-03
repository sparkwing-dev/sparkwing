package client

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// UnavailableRetries is how many times a client repeats a request the server
// answered 503 with a Retry-After header. A daemon holding the runs store
// answers that way while the store is momentarily unreachable, so a heartbeat
// or a node finish waits the wedge out instead of failing the run.
const UnavailableRetries = 3

// MaxUnavailableWait caps one Retry-After delay, so a server naming an hour
// does not park the caller for one.
const MaxUnavailableWait = 5 * time.Second

// do sends req, repeating it while the server answers 503 with a Retry-After
// header it can honour. A 503 without that header is returned as it arrived,
// which is how a server says the condition is not going to clear.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := c.http.Do(req)
		if err != nil || resp.StatusCode != http.StatusServiceUnavailable {
			return resp, err
		}
		wait, ok := retryAfter(resp)
		if !ok || attempt >= UnavailableRetries || !replayable(req) {
			return resp, nil
		}
		next, rewindErr := rewind(req)
		if rewindErr != nil {
			return resp, nil
		}
		if sleepErr := waitFor(req.Context(), wait); sleepErr != nil {
			return resp, nil
		}
		// safety: the answer is only discarded once the retry is certain, so
		// a caller that gets this response back can still read its body.
		drain(resp)
		req = next
	}
}

func replayable(req *http.Request) bool {
	return req.Body == nil || req.GetBody != nil
}

func retryAfter(resp *http.Response) (time.Duration, bool) {
	raw := resp.Header.Get("Retry-After")
	if raw == "" {
		return 0, false
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 {
		return 0, false
	}
	wait := time.Duration(seconds) * time.Second
	if wait > MaxUnavailableWait {
		wait = MaxUnavailableWait
	}
	return wait, true
}

func drain(resp *http.Response) {
	_ = resp.Body.Close()
}

func rewind(req *http.Request) (*http.Request, error) {
	next := req.Clone(req.Context())
	if req.GetBody == nil {
		return next, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	next.Body = body
	return next, nil
}

func waitFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
