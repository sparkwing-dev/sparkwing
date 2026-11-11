package controller

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/web"
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

func setSessionCookies(w http.ResponseWriter, sessionID, csrfToken string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   sessionCookieMaxAge,
	})
	setCSRFCookie(w, csrfToken, sessionCookieMaxAge)
}

func setCSRFCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false, // safety: the page sends the session-bound token as a header and a form field; the session id stays HttpOnly
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	})
}

func clearSessionCookies(w http.ResponseWriter) {
	for _, name := range []string{sessionCookieName, csrfCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Secure:   true,
			HttpOnly: name == sessionCookieName,
			SameSite: http.SameSiteStrictMode,
		})
	}
}

func sessionCookie(r *http.Request) string {
	var value string
	for _, c := range r.Cookies() {
		if c.Name != sessionCookieName {
			continue
		}
		if value != "" || c.Value == "" {
			return ""
		}
		value = c.Value
	}
	return value
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
func validAPIRequestCSRF(r *http.Request, sessionToken string) bool {
	if !sameOriginRequest(r) {
		return false
	}
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil {
		return false
	}
	header := r.Header.Get(csrfHeaderName)
	return constantTimeEqual(header, cookie.Value) && constantTimeEqual(header, sessionToken)
}

func validFormCSRF(r *http.Request) bool {
	if !sameOriginRequest(r) {
		return false
	}
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	return constantTimeEqual(r.PostForm.Get("csrf_token"), cookie.Value)
}

func csrfError(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "invalid CSRF token", http.StatusForbidden)
}
