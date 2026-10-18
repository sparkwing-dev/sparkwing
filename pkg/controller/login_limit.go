package controller

import (
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

const (
	loginClientBurst  = 30
	loginClientWindow = time.Minute
	loginGlobalWindow = time.Minute
	loginGlobalKey    = "all-clients"
	loginGlobalBurst  = 2400
)

type loginLimiter struct {
	clients *ratelimit.Limiter
	global  *ratelimit.Limiter
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		clients: ratelimit.New(loginClientBurst, loginClientWindow),
		global:  ratelimit.New(loginGlobalBurst, loginGlobalWindow),
	}
}

func (l *loginLimiter) admit(r *http.Request) error {
	now := time.Now()
	if !l.clients.Allow(l.client(r), now) {
		return &apierr.Error{Code: apierr.RateLimited, Message: "too many login attempts from this client", Details: map[string]any{"retry_after_ms": loginClientWindow.Milliseconds()}}
	}
	if !l.global.Allow(loginGlobalKey, now) {
		return &apierr.Error{Code: apierr.RateLimited, Message: "too many login attempts", Details: map[string]any{"retry_after_ms": loginGlobalWindow.Milliseconds()}}
	}
	return nil
}

func (l *loginLimiter) client(r *http.Request) string {
	return ratelimit.ClientIP(r)
}

func writeRetryAfter(w http.ResponseWriter, after time.Duration, message string) {
	writeRetryAfterStatus(w, http.StatusTooManyRequests, after, message)
}

// safety: the header is whole seconds, so a sub-second delay truncates to a
// zero that invites the caller straight back; every delay rounds up instead.
func setRetryAfter(w http.ResponseWriter, after time.Duration) {
	w.Header().Set("Retry-After", strconv.FormatInt(retryAfterWholeSeconds(after), 10))
}

func retryAfterWholeSeconds(after time.Duration) int64 {
	seconds := int64(after / time.Second)
	if after%time.Second > 0 || seconds < 1 {
		seconds++
	}
	return seconds
}

func writeRetryAfterStatus(w http.ResponseWriter, status int, after time.Duration, message string) {
	code := apierr.RateLimited
	if status == http.StatusServiceUnavailable {
		code = apierr.Unavailable
	}
	writePolicyError(w, &apierr.Error{Code: code, Message: message, Details: map[string]any{"retry_after_ms": retryAfterWholeSeconds(after) * 1000}})
}
