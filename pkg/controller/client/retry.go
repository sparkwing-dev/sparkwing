package client

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
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
		if err != nil {
			if attempt >= UnavailableRetries || !executionStartRequest(req) || req.Context().Err() != nil || !transientExecutionStartError(err) {
				return resp, err
			}
			next, rewindErr := rewind(req)
			if rewindErr != nil {
				return resp, err
			}
			backoff := time.Duration(1<<attempt) * 100 * time.Millisecond
			if waitErr := waitFor(req.Context(), backoff+retryJitter(backoff)); waitErr != nil {
				return resp, waitErr
			}
			req = next
			continue
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
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

// safety: execution-start is idempotent for one claim generation and attempt
// ordinal, so a lost answer may be repeated without spending another attempt.
func repeatable(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete:
		return req.Body == nil || req.GetBody != nil
	case http.MethodPost:
		return executionStartRequest(req) && (req.Body == nil || req.GetBody != nil)
	default:
		return false
	}
}

func executionStartRequest(req *http.Request) bool {
	return req.Method == http.MethodPost && strings.HasPrefix(req.URL.Path, "/api/v1/runs/") &&
		strings.HasSuffix(req.URL.Path, "/execution-start")
}

func transientExecutionStartError(err error) bool {
	var netErr net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.As(err, &netErr) && netErr.Timeout()
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

// LoadSignal reports the delay a server named when it answered a request with
// a 429 or a 503 it invited the caller to repeat. A loop that polls treats
// such an answer as backpressure, waiting the delay out rather than failing.
func LoadSignal(err error) (time.Duration, bool) {
	var shed *UnavailableError
	if errors.As(err, &shed) {
		return shed.RetryAfter, true
	}
	var limited *RateLimitedError
	if errors.As(err, &limited) {
		return limited.RetryAfter, true
	}
	return 0, false
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

// RateLimitedError reports a 429 the server answered with a Retry-After.
// Like [UnavailableError] it is a load signal rather than a failure of the
// request: a claim or heartbeat loop waits RetryAfter and polls again.
// Unwrap yields the plain controller error.
type RateLimitedError struct {
	// RetryAfter is the delay the server asked for, as it sent it.
	RetryAfter time.Duration
	// Err is the error the same response would have produced without
	// the header.
	Err error
}

func (e *RateLimitedError) Error() string { return e.Err.Error() }

func (e *RateLimitedError) Unwrap() error { return e.Err }

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
