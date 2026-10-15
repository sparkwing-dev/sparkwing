package controller

import (
	"context"
	"net/http"
	"time"
)

type streamDeadlineKey struct{}

// safety: this publishes a deadline setter and sets none, so only an authenticated handler
// extends one. It sits outside the tracing and logging writers, which do not unwrap.
func withStreamDeadlineControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		set := func(deadline time.Time) {
			_ = rc.SetReadDeadline(deadline)
			_ = rc.SetWriteDeadline(deadline)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), streamDeadlineKey{}, set)))
	})
}

// safety: the listener's WriteTimeout covers a whole connection, so a handler
// that streams for longer than one loses the write it was waiting to make.
func extendStreamDeadline(w http.ResponseWriter, r *http.Request, window time.Duration) {
	deadline := time.Now().Add(window)
	if set, ok := r.Context().Value(streamDeadlineKey{}).(func(time.Time)); ok {
		set(deadline)
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
}
