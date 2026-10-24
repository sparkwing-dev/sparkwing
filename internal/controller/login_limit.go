package controller

import (
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
)

const (
	loginClientBurst  = 30
	loginClientWindow = time.Minute
)

type loginLimiter struct{ clients *ratelimit.Limiter }

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{clients: ratelimit.New(loginClientBurst, loginClientWindow)}
}

func (l *loginLimiter) admit(r *http.Request) error {
	if !l.clients.Allow(loginClientKey(ratelimit.ClientIP(r)), time.Now()) {
		return &apierr.Error{Code: apierr.RateLimited, Message: "too many login attempts from this client", Details: map[string]any{"retry_after_ms": loginClientWindow.Milliseconds()}}
	}
	return nil
}

// safety: one IPv6 subscriber holds at least a /64, so a narrower key hands each client unbounded budgets.
func loginClientKey(client string) string {
	address, err := netip.ParseAddr(client)
	if err != nil {
		return client
	}
	if address = address.Unmap(); address.Is4() {
		return address.String()
	}
	return netip.PrefixFrom(address.WithZone(""), 64).Masked().String()
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
	routepolicy.WritePolicyError(w, &apierr.Error{Code: code, Message: message, Details: map[string]any{"retry_after_ms": retryAfterWholeSeconds(after) * 1000}})
}
