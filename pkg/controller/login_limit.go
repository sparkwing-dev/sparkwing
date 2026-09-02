package controller

import (
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	loginClientBurst   = 30
	loginClientWindow  = time.Minute
	loginGlobalWindow  = time.Minute
	loginFailureBurst  = 5
	loginFailureWindow = 15 * time.Minute
	loginGlobalKey     = "all-clients"

	// safety: sizing the listener bucket per argon2 slot bounds hashing work rather than a fleet's real logins.
	loginGlobalPerHashSlot = 600
)

type loginLimiter struct {
	clients  *ratelimit.Limiter
	global   *ratelimit.Limiter
	failures *ratelimit.Limiter
	trusted  []netip.Prefix

	globalBurst int
}

func newLoginLimiter(trusted []netip.Prefix) *loginLimiter {
	globalBurst := loginGlobalPerHashSlot * store.Argon2Slots()
	return &loginLimiter{
		clients:     ratelimit.New(loginClientBurst, loginClientWindow),
		global:      ratelimit.New(globalBurst, loginGlobalWindow),
		failures:    ratelimit.New(loginFailureBurst, loginFailureWindow),
		trusted:     trusted,
		globalBurst: globalBurst,
	}
}

func (l *loginLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		if !l.clients.Allow(l.client(r), now) {
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

func (l *loginLimiter) client(r *http.Request) string {
	return ratelimit.ClientIP(r, l.trusted)
}

// safety: keying the failure budget on the account alone would let any stranger lock a named user out of the dashboard.
func failureKey(account, client string) string {
	return account + "|" + client
}

func (l *loginLimiter) accountAllowed(account, client string, now time.Time) bool {
	return l.failures.Peek(failureKey(account, client), now)
}

func (l *loginLimiter) accountFailed(account, client string, now time.Time) {
	l.failures.Penalize(failureKey(account, client), now)
}

func writeRetryAfter(w http.ResponseWriter, after time.Duration, message string) {
	setRetryAfter(w, after)
	writeError(w, http.StatusTooManyRequests, errors.New(message))
}

func setRetryAfter(w http.ResponseWriter, after time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(after.Seconds())))
}

func writeRetryAfterStatus(w http.ResponseWriter, status int, after time.Duration, message string) {
	setRetryAfter(w, after)
	writeError(w, status, errors.New(message))
}
