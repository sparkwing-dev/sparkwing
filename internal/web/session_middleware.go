package web

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// sessionAuthMiddleware gates /api/v1/* and SPA routes behind a session
// cookie when RequireLogin is set. Bearer tokens bypass the cookie
// lookup so scripts and agents authenticate via the upstream controller.
func sessionAuthMiddleware(opts HandlerOptions, next http.Handler) http.Handler {
	// Disabled-by-default keeps the laptop-local dev loop working: with
	// no tokens minted, a login redirect would loop forever.
	if !opts.RequireLogin || opts.ControllerURL == "" {
		return next
	}
	cache := newSessionCache(60 * time.Second)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			redirectOrUnauth(w, r)
			return
		}
		sess, err := cache.lookup(r.Context(), opts.ControllerURL, cookie.Value)
		if err != nil {
			clearSessionCookies(w)
			redirectOrUnauth(w, r)
			return
		}
		r = r.WithContext(contextWithWebPrincipal(r.Context(), sess))
		next.ServeHTTP(w, r)
	})
}

// redirectOrUnauth sends a browser to /login (303) and an XHR/API caller
// to 401, distinguished by the Accept header and path prefix.
func redirectOrUnauth(w http.ResponseWriter, r *http.Request) {
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") ||
		strings.HasPrefix(r.URL.Path, "/api/") {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	next := r.URL.Path
	if r.URL.RawQuery != "" {
		next = next + "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, "/login?next="+next, http.StatusSeeOther)
}

// webPrincipal is the logged-in user stamped on the request context.
type webPrincipal struct {
	Name      string
	Scopes    []string
	ExpiresAt time.Time
}

type webPrincipalCtxKey struct{}

func contextWithWebPrincipal(ctx context.Context, sess *sessionResp) context.Context {
	return context.WithValue(ctx, webPrincipalCtxKey{}, &webPrincipal{
		Name:      sess.Principal,
		Scopes:    sess.Scopes,
		ExpiresAt: time.Unix(sess.ExpiresAt, 0).UTC(),
	})
}

// WebPrincipalFromContext returns the logged-in user from the request
// context, if any.
func WebPrincipalFromContext(ctx context.Context) (*webPrincipal, bool) {
	p, ok := ctx.Value(webPrincipalCtxKey{}).(*webPrincipal)
	return p, ok
}

type sessionCacheEntry struct {
	sess    *sessionResp
	expires time.Time
}

type sessionCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]*sessionCacheEntry
}

func newSessionCache(ttl time.Duration) *sessionCache {
	return &sessionCache{ttl: ttl, m: map[string]*sessionCacheEntry{}}
}

func (c *sessionCache) lookup(ctx context.Context, controllerURL, sessionID string) (*sessionResp, error) {
	c.mu.Lock()
	e := c.m[sessionID]
	c.mu.Unlock()
	if e != nil && time.Now().Before(e.expires) {
		return e.sess, nil
	}
	sess, err := controllerResolveSession(ctx, controllerURL, sessionID)
	if err != nil {
		c.mu.Lock()
		delete(c.m, sessionID)
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Lock()
	c.m[sessionID] = &sessionCacheEntry{
		sess:    sess,
		expires: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()
	return sess, nil
}
