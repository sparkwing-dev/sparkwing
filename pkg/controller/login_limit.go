package controller

import (
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

const (
	loginClientBurst   = 30
	loginClientWindow  = time.Minute
	loginGlobalBurst   = 120
	loginGlobalWindow  = time.Minute
	loginFailureBurst  = 5
	loginFailureWindow = 15 * time.Minute
	loginGlobalKey     = "all-clients"
)

type loginLimiter struct {
	clients  *ratelimit.Limiter
	global   *ratelimit.Limiter
	failures *ratelimit.Limiter
	trusted  []netip.Prefix
}

func newLoginLimiter(trusted []netip.Prefix) *loginLimiter {
	return &loginLimiter{
		clients:  ratelimit.New(loginClientBurst, loginClientWindow),
		global:   ratelimit.New(loginGlobalBurst, loginGlobalWindow),
		failures: ratelimit.New(loginFailureBurst, loginFailureWindow),
		trusted:  trusted,
	}
}

func (l *loginLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		if !l.clients.Allow(ratelimit.ClientIP(r, l.trusted), now) {
			writeRetryAfter(w, loginClientWindow, "too many login attempts from this client")
			return
		}
		if !l.global.Allow(loginGlobalKey, now) {
			writeRetryAfter(w, loginGlobalWindow, "too many login attempts")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *loginLimiter) accountAllowed(account string, now time.Time) bool {
	return l.failures.Peek(account, now)
}

func (l *loginLimiter) accountFailed(account string, now time.Time) {
	l.failures.Penalize(account, now)
}

func writeRetryAfter(w http.ResponseWriter, after time.Duration, message string) {
	w.Header().Set("Retry-After", strconv.Itoa(int(after.Seconds())))
	writeError(w, http.StatusTooManyRequests, errors.New(message))
}
