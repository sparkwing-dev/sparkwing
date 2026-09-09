// Package streamhttp bounds individual HTTP stream writes without limiting stream lifetime.
package streamhttp

import (
	"errors"
	"net/http"
	"time"
)

type writer struct {
	response   http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
}

// NewWriter clears the response deadline while idle and bounds each Write and
// Flush operation. Flush through this writer so buffered output is also bounded.
func NewWriter(w http.ResponseWriter, timeout time.Duration) (*writer, error) {
	out := &writer{response: w, controller: http.NewResponseController(w), timeout: timeout}
	if err := out.setDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return out, nil
}

func (w *writer) setDeadline(deadline time.Time) error {
	err := w.controller.SetWriteDeadline(deadline)
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func (w *writer) Write(p []byte) (int, error) {
	if err := w.setDeadline(time.Now().Add(w.timeout)); err != nil {
		return 0, err
	}
	n, err := w.response.Write(p)
	return n, errors.Join(err, w.setDeadline(time.Time{}))
}

func (w *writer) Flush() error {
	if err := w.setDeadline(time.Now().Add(w.timeout)); err != nil {
		return err
	}
	err := w.controller.Flush()
	return errors.Join(err, w.setDeadline(time.Time{}))
}
