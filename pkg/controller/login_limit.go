package controller

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	loginClientBurst  = 30
	loginClientWindow = time.Minute
	loginGlobalWindow = time.Minute
	loginGlobalKey    = "all-clients"

	// safety: sizing the listener bucket per argon2 slot bounds hashing work rather than a fleet's real logins.
	loginGlobalPerHashSlot = 600
)

type loginLimiter struct {
	clients *ratelimit.Limiter
	global  *ratelimit.Limiter

	globalBurst int
}

func newLoginLimiter() *loginLimiter {
	globalBurst := loginGlobalPerHashSlot * store.Argon2Slots()
	return &loginLimiter{
		clients:     ratelimit.New(loginClientBurst, loginClientWindow),
		global:      ratelimit.New(globalBurst, loginGlobalWindow),
		globalBurst: globalBurst,
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
	setRetryAfter(w, after)
	writeError(w, http.StatusTooManyRequests, errors.New(message))
}

// safety: the header is whole seconds, so a sub-second delay truncates to a
// zero that invites the caller straight back; every delay rounds up instead.
func setRetryAfter(w http.ResponseWriter, after time.Duration) {
	seconds := int64(after / time.Second)
	if after%time.Second > 0 || seconds < 1 {
		seconds++
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
}

func writeRetryAfterStatus(w http.ResponseWriter, status int, after time.Duration, message string) {
	setRetryAfter(w, after)
	writeError(w, status, errors.New(message))
}
