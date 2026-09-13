package client

import (
	"context"
	"math/rand/v2"
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

// safety: a 503 without Retry-After is returned as it arrived, which is how a
// server says the condition will not clear; only a server that invites the
// caller back gets another attempt.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := c.http.Do(req)
		if err != nil || resp.StatusCode != http.StatusServiceUnavailable {
			return resp, err
		}
		wait, ok := retryAfter(resp)
		if !ok || attempt >= UnavailableRetries || !repeatable(req) {
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

// safety: an ingress can answer 503 after the handler ran, so only a method
// the server must treat as repeatable is sent twice. No route this client
// posts to accepts an idempotency key, so no POST qualifies: a replayed
// submission would be a second run.
func repeatable(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete:
		return req.Body == nil || req.GetBody != nil
	default:
		return false
	}
}

func retryAfter(resp *http.Response) (time.Duration, bool) {
	wait, ok := parseRetryAfter(resp)
	if !ok {
		return 0, false
	}
	if wait > MaxUnavailableWait {
		wait = MaxUnavailableWait
	}
	return wait + retryJitter(wait), true
}

func parseRetryAfter(resp *http.Response) (time.Duration, bool) {
	raw := resp.Header.Get("Retry-After")
	if raw == "" {
		return 0, false
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

// UnavailableError reports a 503 the server invited the caller to repeat
// by naming a Retry-After. It is a load signal rather than a failure of
// the request: a claim or heartbeat loop waits RetryAfter and polls
// again instead of reporting an error. Unwrap yields the plain
// controller error, so a caller that does not care reads it as before.
type UnavailableError struct {
	// RetryAfter is the delay the server asked for, as it sent it.
	RetryAfter time.Duration
	// Err is the error the same response would have produced without
	// the header.
	Err error
}

func (e *UnavailableError) Error() string { return e.Err.Error() }

func (e *UnavailableError) Unwrap() error { return e.Err }

// safety: the spread is added to the server's delay rather than drawn across
// it, because a caller that woke early would answer an invitation the server
// has not yet made.

// #nosec G404 -- retry spread, not a security decision
func retryJitter(wait time.Duration) time.Duration {
	if wait <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(wait/4) + 1))
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
