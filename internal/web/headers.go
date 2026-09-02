package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
)

const hstsValue = "max-age=31536000; includeSubDomains"

type cspNonceCtxKey struct{}

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := newCSPNonce()
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy(nonce))
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		// safety: no-referrer sends Origin: null on same-origin form posts,
		// which the login CSRF check reads as a cross-site submission.
		h.Set("Referrer-Policy", "same-origin")
		if cookieSecure {
			h.Set("Strict-Transport-Security", hstsValue)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), cspNonceCtxKey{}, nonce)))
	})
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
