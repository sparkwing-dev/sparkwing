package client

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

const unavailableRetries = 3

type gatewayError struct{ failure *apierr.Error }

func (e *gatewayError) Error() string { return e.failure.Error() }
func (e *gatewayError) Unwrap() error { return e.failure }

// RetryBackoff adds exponential jitter above the polling floor, capped at 5s
// or the floor when it is longer. Callers retain recovery deadlines and advice.
func RetryBackoff(attempt int, floor time.Duration) time.Duration {
	floor = max(floor, 0)
	ceiling := min(100*time.Millisecond<<min(max(attempt, 0), 6), max(5*time.Second, floor)-floor)
	// #nosec G404 -- reconnect jitter carries no authority.
	return floor + ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)+1))
}

// safety: a controller outage cannot end held work; only read operations wait for reconnection.
func retryHeldRead(ctx context.Context, read func() error) error {
	for attempt := 0; ; attempt++ {
		err := read()
		if err == nil {
			return nil
		}
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		retry := RetryableTransportError(err)
		var failure *apierr.Error
		if errors.As(err, &failure) {
			retry = failure.Code == hosterr.Unavailable
		}
		if !retry {
			return err
		}
		delay := RetryBackoff(attempt, time.Second)
		if advised, ok := hosterr.RetryDelay(err); ok {
			delay = max(delay, advised)
		}
		if err := waitFor(ctx, delay); err != nil {
			return context.Cause(ctx)
		}
	}
}

// RetryableTransportError identifies interrupted controller transports and
// temporary 5xx unavailability, including controller replacement.
func RetryableTransportError(err error) bool {
	var gateway *gatewayError
	var netErr net.Error
	var operation *net.OpError
	var path *os.PathError
	return errors.As(err, &gateway) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.As(err, &netErr) && netErr.Timeout() ||
		errors.As(err, &operation) && operation.Op == "dial" && !errors.Is(err, os.ErrPermission) && (strings.HasPrefix(operation.Net, "tcp") || strings.HasPrefix(operation.Net, "unix") && errors.Is(err, os.ErrNotExist)) ||
		errors.As(err, &path) && path.Op == "connect" && errors.Is(err, os.ErrNotExist)
}

// safety: bounds a parsed Retry-After above every cap a caller applies, so a
// huge header clamps here instead of overflowing the duration.
const maxRetryAfter = time.Hour

// safety: the seconds are clamped before they are multiplied, because a
// header near the int64 limit wraps a Duration negative or small, which a
// caller would read as an invitation to come straight back.
func parseRetryAfter(resp *http.Response) (time.Duration, bool) {
	raw := resp.Header.Get("Retry-After")
	if delay, ok := parseDelaySeconds(raw); ok {
		return delay, true
	}
	if at, err := http.ParseTime(raw); err == nil {
		return min(max(time.Until(at), 0), maxRetryAfter), true
	}
	return 0, false
}

func parseDelaySeconds(raw string) (time.Duration, bool) {
	if raw == "" {
		return 0, false
	}
	seconds, err := strconv.ParseUint(raw, 10, 64)
	if errors.Is(err, strconv.ErrRange) {
		return maxRetryAfter, true
	}
	if err != nil {
		return 0, false
	}
	if seconds >= uint64(maxRetryAfter/time.Second) {
		return maxRetryAfter, true
	}
	return time.Duration(seconds) * time.Second, true
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
