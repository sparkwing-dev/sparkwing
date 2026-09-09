// Package streamhttp bounds individual HTTP stream writes without limiting stream lifetime.
package streamhttp

import (
	"errors"
	"io"
	"net/http"
	"time"
)

type writer struct {
	response   http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
}

// NewWriter refreshes the response deadline before each write. Flush through the
// original response after writing; it shares the same deadline.
func NewWriter(w http.ResponseWriter, timeout time.Duration) io.Writer {
	return &writer{response: w, controller: http.NewResponseController(w), timeout: timeout}
}

func (w *writer) Write(p []byte) (int, error) {
	if err := w.controller.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return 0, err
	}
	return w.response.Write(p)
}
