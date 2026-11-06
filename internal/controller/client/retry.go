package client

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const unavailableRetries = 3

// RetryableTransportError identifies interrupted controller transports, including
// a local socket temporarily absent while its controller restarts.
func RetryableTransportError(err error) bool {
	var netErr net.Error
	var operation *net.OpError
	var path *os.PathError
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.As(err, &netErr) && netErr.Timeout() ||
		errors.As(err, &operation) && operation.Op == "dial" && strings.HasPrefix(operation.Net, "unix") && errors.Is(err, os.ErrNotExist) ||
		errors.As(err, &path) && path.Op == "connect" && errors.Is(err, os.ErrNotExist)
}

// safety: bounds a parsed Retry-After above every cap a caller applies, so a
// huge header clamps here instead of overflowing the duration.
const maxRetryAfter = time.Hour

// safety: the seconds are clamped before they are multiplied, because a
// header near the int64 limit wraps a Duration negative or small, which a
// caller would read as an invitation to come straight back.
func parseRetryAfter(resp *http.Response) (time.Duration, bool) {
	return parseDelaySeconds(resp.Header.Get("Retry-After"))
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
