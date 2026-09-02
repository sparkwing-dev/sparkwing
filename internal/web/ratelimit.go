package web

import (
	"net/http"
	"net/netip"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

const (
	loginRateBurst  = 10
	loginRateWindow = 60 * time.Second
)

func rateLimitMiddleware(l *ratelimit.Limiter, trustedProxyCIDRs []netip.Prefix, next http.Handler) http.Handler {
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			l.GC(time.Now())
		}
	}()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		if !l.Allow(ratelimit.ClientIP(r, trustedProxyCIDRs), time.Now()) {
			w.Header().Set("Retry-After", "60")
			http.Error(w,
				"too many login attempts; try again in a minute",
				http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
