package web

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// sessionAuthMiddleware gates /api/v1/* and SPA routes behind a session
// cookie when RequireLogin is set. Service credentials stay behind the
// reverse proxy and cannot bypass a browser session after logout.
func sessionAuthMiddleware(opts HandlerOptions, bundleFS fs.FS, next http.Handler) http.Handler {
	if !loginRequired(opts) {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if immutableStaticAssetRequest(r, bundleFS) {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			redirectOrUnauth(w, r)
			return
		}
		sess, err := controllerResolveSession(r.Context(), authControllerURL(opts), cookie.Value)
		if err != nil {
			if errors.Is(err, errInvalidControllerSession) {
				clearSessionCookies(w)
				redirectOrUnauth(w, r)
			} else {
				sessionBackendError(w)
			}
			return
		}
		if unsafeAPIRequest(r) && !validAPIRequestCSRF(r, sess.CSRFToken) {
			csrfError(w)
			return
		}
		r = r.WithContext(contextWithWebPrincipal(r.Context(), sess))
		next.ServeHTTP(w, r)
	})
}

func immutableStaticAssetRequest(r *http.Request, bundleFS fs.FS) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	if !strings.HasPrefix(p, "/_next/static/") || path.Clean(p) != p || strings.Contains(p, `\`) {
		return false
	}
	info, err := fs.Stat(bundleFS, strings.TrimPrefix(p, "/"))
	return err == nil && !info.IsDir()
}

func unsafeAPIRequest(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	default:
		return true
	}
}

func validAPIRequestCSRF(r *http.Request, sessionToken string) bool {
	if !sameOriginRequest(r) {
		return false
	}
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil {
		return false
	}
	headerToken := r.Header.Get(csrfHeaderName)
	return constantTimeEqual(headerToken, cookie.Value) && constantTimeEqual(headerToken, sessionToken)
}

func loginRequired(opts HandlerOptions) bool {
	return opts.RequireLogin
}

// redirectOrUnauth sends a browser to /login (303) and an XHR/API caller
// to 401, distinguished by the Accept header and path prefix.
func redirectOrUnauth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") ||
		strings.HasPrefix(r.URL.Path, "/api/") {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	next := safeNext(r.URL.RequestURI())
	query := url.Values{"next": []string{next}}
	http.Redirect(w, r, "/login?"+query.Encode(), http.StatusSeeOther)
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
