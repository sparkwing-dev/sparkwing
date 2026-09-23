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

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
)

func sessionAuthMiddleware(opts HandlerOptions, bundleFS fs.FS, next http.Handler) http.Handler {
	if !loginRequired(opts) {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if immutableStaticAssetRequest(r, bundleFS) {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(cookieName(sessionCookieName, cookiesSecure(opts)))
		if err != nil || cookie.Value == "" {
			redirectOrUnauth(w, r)
			return
		}
		sess, err := resolveDashboardSession(r.Context(), opts, cookie.Value)
		if err != nil {
			if errors.Is(err, errInvalidControllerSession) {
				clearSessionCookies(w, cookiesSecure(opts))
				redirectOrUnauth(w, r)
			} else {
				sessionBackendError(w)
			}
			return
		}
		if unsafeAPIRequest(r) && !validAPIRequestCSRF(r, sess.CSRFToken, cookiesSecure(opts)) {
			csrfError(w)
			return
		}
		r = r.WithContext(contextWithWebPrincipal(r.Context(), sess, cookie.Value))
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

func validAPIRequestCSRF(r *http.Request, sessionToken string, secure bool) bool {
	if !sameOriginRequest(r) {
		return false
	}
	cookie, err := r.Cookie(cookieName(csrfCookieName, secure))
	if err != nil {
		return false
	}
	headerToken := r.Header.Get(csrfHeaderName)
	return constantTimeEqual(headerToken, cookie.Value) && constantTimeEqual(headerToken, sessionToken)
}

func loginRequired(opts HandlerOptions) bool {
	return opts.RequireLogin
}

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

type webPrincipal struct {
	Name      string
	Scopes    []string
	ExpiresAt time.Time

	sessionID string
}

type webPrincipalCtxKey struct{}

func contextWithWebPrincipal(ctx context.Context, sess *sessionResp, sessionID string) context.Context {
	return context.WithValue(ctx, webPrincipalCtxKey{}, &webPrincipal{
		Name:      sess.Principal,
		Scopes:    sess.Scopes,
		ExpiresAt: time.Unix(sess.ExpiresAt, 0).UTC(),
		sessionID: sessionID,
	})
}

func sessionIDFromContext(ctx context.Context) string {
	if p, ok := WebPrincipalFromContext(ctx); ok {
		return p.sessionID
	}
	return ""
}

// SessionForwardingTransport sends a request made on behalf of a signed-in
// browser with that browser's own controller session, replacing whatever
// Authorization an inner client set. A request whose context carries no
// session goes out unchanged. Wrap the transport of any controller client the
// dashboard calls while serving a request, for example:
//
//	hc := &http.Client{Transport: web.SessionForwardingTransport(http.DefaultTransport)}
//	c := client.NewWithToken(controllerURL, hc, serviceToken)
func SessionForwardingTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return sessionForwardingTransport{base: base}
}

type sessionForwardingTransport struct{ base http.RoundTripper }

func (t sessionForwardingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	id := sessionIDFromContext(req.Context())
	if id == "" {
		return t.base.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", sessionAuthorization(id))
	return t.base.RoundTrip(req)
}

func sessionAuthorization(sessionID string) string {
	return "Session " + sessionID
}

func WebPrincipalFromContext(ctx context.Context) (*webPrincipal, bool) {
	p, ok := ctx.Value(webPrincipalCtxKey{}).(*webPrincipal)
	return p, ok
}

func logsProxy(opts HandlerOptions) http.Handler {
	return logsProxyAllowList(withLogsIdentityHeader(
		controllerProxy(opts.LogsURL, opts.Token, loginRequired(opts), true)))
}

// DurableLogStore reads a logs service on behalf of the signed-in browser
// whose request is being served. The logs service asks the controller whether
// the caller's team owns each run, so a read made with the dashboard's own
// token would answer for the operator's team instead of the user's. The client
// has no overall timeout, because a log stream stays open.
func DurableLogStore(logsURL, token string) storage.LogStore {
	return sparkwinglogs.New(logsURL, &http.Client{
		Transport: SessionForwardingTransport(otelutil.WrapTransport(nil)),
	}, token)
}
