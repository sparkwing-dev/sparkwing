// Package apierrhttp writes apierr envelopes as HTTP responses for the controller and local servers.
package apierrhttp

import (
	"bytes"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// Write writes the envelope and derives status from Code, including retry_after_ms as Retry-After.
// Encode failures are returned to the caller for logging; this writer never emits plain text.
func Write(w http.ResponseWriter, failure *apierr.Error) error {
	var encoded bytes.Buffer
	if err := apierr.WriteJSON(&encoded, failure); err != nil {
		return err
	}
	status := apierr.Status(failure.Code)
	if status >= http.StatusInternalServerError {
		NoteResponseError(w, failure)
	}
	w.Header().Set("Content-Type", "application/json")
	if delay := apierr.RetryAfter(failure); delay != "" {
		w.Header().Set("Retry-After", delay)
	}
	w.WriteHeader(status)
	n, err := w.Write(encoded.Bytes())
	if err == nil && n != encoded.Len() {
		return io.ErrShortWrite
	}
	return err
}

// NoteResponseError retains the first cause for the request's masked diagnostic.
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
