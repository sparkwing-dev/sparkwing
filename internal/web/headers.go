package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

const hstsValue = "max-age=31536000; includeSubDomains"

type cspNonceCtxKey struct{}

type requestTLSCtxKey struct{}

// SecurityHeadersMiddleware wraps next with the dashboard's response
// security headers, for callers that mount their own routes beside the
// handler HandlerFromOptions builds. Pass the same options the handler
// got so the TLS-dependent headers agree.
func SecurityHeadersMiddleware(opts HandlerOptions, next http.Handler) http.Handler {
	return securityHeadersMiddleware(opts, next)
}

func securityHeadersMiddleware(opts HandlerOptions, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := newCSPNonce()
		overTLS := requestOverTLS(r, opts)
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy(nonce))
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		// safety: no-referrer sends Origin: null on same-origin form posts,
		// which the login CSRF check reads as a cross-site submission.
		h.Set("Referrer-Policy", "same-origin")
		// safety: HSTS pins the host and every subdomain of it for a year, so it
		// needs evidence that this dashboard is reachable over TLS at all.
		if overTLS {
			h.Set("Strict-Transport-Security", hstsValue)
		}
		ctx := context.WithValue(r.Context(), cspNonceCtxKey{}, nonce)
		ctx = context.WithValue(ctx, requestTLSCtxKey{}, overTLS)
		ctx = context.WithValue(ctx, clientIPCtxKey{}, ratelimit.ClientIP(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestOverTLS(r *http.Request, opts HandlerOptions) bool {
	return opts.HSTS || r.TLS != nil || forwardedHTTPS(r)
}

func forwardedHTTPS(r *http.Request) bool {
	if !ratelimit.FromTrustedListener(r) {
		return false
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if comma := strings.IndexByte(proto, ','); comma >= 0 {
		proto = proto[:comma]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

func requestOverTLSFrom(ctx context.Context) bool {
	overTLS, _ := ctx.Value(requestTLSCtxKey{}).(bool)
	return overTLS
}

// safety: the exported bundle and the server-rendered pages carry inline
// styles, so style-src stays permissive while script-src rides the nonce.
func contentSecurityPolicy(nonce string) string {
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' 'nonce-" + nonce + "'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"frame-ancestors 'none'",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'self'",
	}, "; ")
}

func newCSPNonce() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func cspNonceFrom(ctx context.Context) string {
	nonce, _ := ctx.Value(cspNonceCtxKey{}).(string)
	return nonce
}

type clientIPCtxKey struct{}

var controllerTransport = ControllerTransport(http.DefaultTransport)

// ControllerTransport sends the address of the browser being served as
// X-Real-IP, replacing any the caller set, so the controller's trusted
// listener keys that browser's budgets and audit records on it. Wrap only
// the transport of a controller client: no other service reads the header.
func ControllerTransport(base http.RoundTripper) http.RoundTripper {
	return relayTransport{base: base}
}

type relayTransport struct{ base http.RoundTripper }

func (t relayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clientIP, _ := req.Context().Value(clientIPCtxKey{}).(string)
	req = req.Clone(req.Context())
	if clientIP == "" {
		req.Header.Del("X-Real-IP")
	} else {
		req.Header.Set("X-Real-IP", clientIP)
	}
	return t.base.RoundTrip(req)
}
