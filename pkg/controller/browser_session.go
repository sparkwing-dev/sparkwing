package controller

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: host-only scoping stops a sibling host under the registrable domain reading these cookies and does not
// stop one writing a same-named cookie with Domain and a longer Path, which sorts first in the Cookie header and is
// the duplicate r.Cookie returns. A browser refuses a __Host- cookie carrying Domain, and that write is the attack.
const hostPrefix = "__Host-"

const (
	sessionCookieName = hostPrefix + "sw_session"
	csrfCookieName    = hostPrefix + "sw_csrf"
	csrfHeaderName    = "X-CSRF-Token"
)

// safety: the session cookie outlives the store's sliding session TTL, so the
// store's expiry, not the browser's, ends a session.
const sessionCookieMaxAge = int(30 * 24 * time.Hour / time.Second)

// safety: a browser discards a __Host- cookie that is not Secure, so the plain-HTTP escape has to drop the prefix
// rather than keep it or it signs nobody in. Only that escape drops Secure.
func cookieName(name string, secure bool) string {
	if secure {
		return name
	}
	return strings.TrimPrefix(name, hostPrefix)
}

func (s *Server) cookiesSecure() bool {
	return s.dashboard == nil || !s.dashboard.InsecureCookies
}

func setSessionCookies(w http.ResponseWriter, sessionID, csrfToken string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName(sessionCookieName, secure),
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   sessionCookieMaxAge,
	})
	setCSRFCookie(w, csrfToken, secure, sessionCookieMaxAge)
}

func setCSRFCookie(w http.ResponseWriter, token string, secure bool, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName(csrfCookieName, secure),
		Value:    token,
		Path:     "/",
		HttpOnly: false, // safety: the page sends the session-bound token as a header and a form field; the session id stays HttpOnly
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	})
}

func clearSessionCookies(w http.ResponseWriter, secure bool) {
	for _, name := range []string{sessionCookieName, csrfCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName(name, secure),
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Secure:   secure,
			HttpOnly: name == sessionCookieName,
			SameSite: http.SameSiteStrictMode,
		})
	}
}

func sessionCookie(r *http.Request, secure bool) string {
	c, err := r.Cookie(cookieName(sessionCookieName, secure))
	if err != nil {
		return ""
	}
	return c.Value
}

func newCSRFToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func constantTimeEqual(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// safety: without TLS evidence the process cannot know its external scheme, so
// the host comparison carries the check rather than reject the live origin.
func sameOriginRequest(r *http.Request) bool {
	raw := r.Header.Get("Origin")
	if raw == "" {
		raw = r.Referer()
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Host == "" || u.User != nil || !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	return !web.RequestOverTLS(r.Context()) || strings.EqualFold(u.Scheme, "https")
}

func unsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	}
	return true
}

// safety: the header must match both the cookie and the session's own token,
// so a token planted in the cookie jar alone authorizes nothing.
func validAPIRequestCSRF(r *http.Request, sessionToken string, secure bool) bool {
	if !sameOriginRequest(r) {
		return false
	}
	cookie, err := r.Cookie(cookieName(csrfCookieName, secure))
	if err != nil {
		return false
	}
	header := r.Header.Get(csrfHeaderName)
	return constantTimeEqual(header, cookie.Value) && constantTimeEqual(header, sessionToken)
}

func validFormCSRF(r *http.Request, secure bool) bool {
	if !sameOriginRequest(r) {
		return false
	}
	cookie, err := r.Cookie(cookieName(csrfCookieName, secure))
	if err != nil || cookie.Value == "" {
		return false
	}
	return constantTimeEqual(r.PostForm.Get("csrf_token"), cookie.Value)
}

func csrfError(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "invalid CSRF token", http.StatusForbidden)
}

type browserSession struct {
	principal *Principal
	csrfToken string
}

type browserSessionKey struct{}

func withBrowserSession(ctx context.Context, sess *browserSession) context.Context {
	return context.WithValue(ctx, browserSessionKey{}, sess)
}

func browserSessionFrom(ctx context.Context) *browserSession {
	sess, _ := ctx.Value(browserSessionKey{}).(*browserSession)
	return sess
}

// safety: a cookie is the one credential a browser attaches on its own, so it
// authenticates only a request that carries no Authorization of its own, and
// it never authenticates a write that fails the CSRF check. Bearer, claim and
// Session-header requests reach the authenticator exactly as they arrived.
func (s *Server) cookieSessions(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secure := s.cookiesSecure()
		raw := sessionCookie(r, secure)
		if raw == "" || r.Header.Get("Authorization") != "" {
			next.ServeHTTP(w, r)
			return
		}
		sess, err := s.resolveBrowserSession(r.Context(), raw)
		if err != nil {
			s.refuseBrowserSession(w, err, secure)
			return
		}
		if unsafeMethod(r.Method) && !validAPIRequestCSRF(r, sess.csrfToken, secure) {
			csrfError(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(withBrowserSession(r.Context(), sess)))
	})
}

func (s *Server) resolveBrowserSession(ctx context.Context, raw string) (*browserSession, error) {
	p, sess, err := s.lookupSession(ctx, raw, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return &browserSession{principal: p, csrfToken: sess.CSRFToken}, nil
}

// safety: a backend fault answered 401 would read as expiry and clear a live
// session's cookies, so only a refused session ends the browser's.
func (s *Server) refuseBrowserSession(w http.ResponseWriter, err error, secure bool) {
	w.Header().Set("Cache-Control", "no-store")
	if errors.Is(err, store.ErrSessionBackend) {
		s.logger.Error("session.unavailable", "error", err.Error())
		setRetryAfter(w, authUnavailableRetryAfter)
		writeAuthError(w, http.StatusServiceUnavailable, authErrorBody{
			Code: "unavailable", Message: "authentication is temporarily unavailable",
		})
		return
	}
	clearSessionCookies(w, secure)
	writeAuthError(w, http.StatusUnauthorized, authErrorBody{Code: "unauthenticated", Message: err.Error()})
}
