// Package apierrhttp writes apierr envelopes as HTTP responses for the controller and local servers.
package apierrhttp

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// ErrStoppedRetryable marks a cause noted with NoteResponseError when the request's context ended but the handler
// still answers a retryable error: the controller then delivers that status instead of recording a closed request.
// Join it with the context error, as in errors.Join(apierrhttp.ErrStoppedRetryable, ctx.Err()).
var ErrStoppedRetryable = errors.New("the request context ended before the answer; the client should retry")

// Write writes the envelope and derives status from Code, including retry_after_ms as Retry-After.
// Encode failures are returned to the caller for logging; this writer never emits plain text.
func Write(w http.ResponseWriter, failure *apierr.Error) error {
	var encoded bytes.Buffer
	if err := hosterr.WriteJSON(&encoded, failure); err != nil {
		return err
	}
	status := hosterr.Status(failure.Code)
	if status >= http.StatusInternalServerError {
		NoteResponseError(w, failure)
	}
	w.Header().Set("Content-Type", "application/json")
	if delay := hosterr.RetryAfter(failure); delay != "" {
		w.Header().Set("Retry-After", delay)
	}
	w.WriteHeader(status)
	n, err := w.Write(encoded.Bytes())
	if err == nil && n != encoded.Len() {
		return io.ErrShortWrite
	}
	return err
}

// NoteResponseError passes a private cause to the request's masked diagnostic recorder.
func NoteResponseError(w http.ResponseWriter, err error) {
	for {
		if recorder, ok := w.(interface{ ResponseError(error) }); ok {
			recorder.ResponseError(err)
			return
		}
		wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = wrapper.Unwrap()
	}
}
