package web

import (
	"net/http"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

const (
	loginRateBurst    = 10
	loginRateWindow   = 60 * time.Second
	loginRateGCPeriod = 5 * time.Minute
)

func rateLimitMiddleware(l *ratelimit.Limiter, trustedProxyCIDRs []netip.Prefix, next http.Handler) http.Handler {
	return rateLimitMiddlewareEvery(l, trustedProxyCIDRs, next, loginRateGCPeriod)
}

func rateLimitMiddlewareEvery(l *ratelimit.Limiter, trustedProxyCIDRs []netip.Prefix, next http.Handler, gcPeriod time.Duration) http.Handler {
	var lastGC atomic.Int64
	lastGC.Store(time.Now().UnixNano())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		// safety: sweeping from the request path rather than a ticker keeps a handler
		// build from starting a goroutine that nothing can stop; the bucket map is
		// already bounded, so an idle listener only defers the sweep.
		if prev := lastGC.Load(); now.Sub(time.Unix(0, prev)) >= gcPeriod && lastGC.CompareAndSwap(prev, now.UnixNano()) {
			l.GC(now)
		}
		if r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		if !l.Allow(ratelimit.ClientIP(r, trustedProxyCIDRs), now) {
			w.Header().Set("Retry-After", "60")
			http.Error(w,
				"too many login attempts; try again in a minute",
				http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
