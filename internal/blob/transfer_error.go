package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
)

type transferFailure struct {
	method, host, class string
	attempts            int
	retry               bool
	notRetriedReason    string
	cause               error
}

func (e *transferFailure) Error() string {
	message := fmt.Sprintf("signed blob %s %s: %s (attempts=%d)", e.method, e.host, e.class, e.attempts)
	if e.notRetriedReason != "" {
		message += "; " + e.notRetriedReason + "; not retried"
	}
	return message
}
func (e *transferFailure) Unwrap() error { return e.cause }

// safety: request errors retain signed URLs through unwrap chains; only fixed classes and safe sentinels escape.
func newTransferFailure(method, raw string, err error) *transferFailure {
	u, parseErr := url.Parse(raw)
	host := "unknown"
	if parseErr == nil && u != nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	e := &transferFailure{method: method, host: host, class: "transport_error", attempts: 1}
	for _, c := range []struct {
		err   error
		class string
		retry bool
	}{
		{context.Canceled, "cancelled", false},
		{context.DeadlineExceeded, "deadline_exceeded", false},
		{ErrIntegrity, "integrity", false},
		{io.EOF, "EOF", true},
		{io.ErrUnexpectedEOF, "unexpected_EOF", true},
		{syscall.ECONNRESET, "connection_reset", true},
		{syscall.EPIPE, "broken_pipe", true},
		{net.ErrClosed, "connection_closed", true},
	} {
		if errors.Is(err, c.err) {
			e.class, e.retry, e.cause = c.class, c.retry, c.err
			return e
		}
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		e.class, e.retry = "timeout", true
		return e
	}
	var op *net.OpError
	if errors.As(err, &op) {
		e.class, e.retry = "network", true
	}
	return e
}
