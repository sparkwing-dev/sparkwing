package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
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

// safety: a browser discards a __Host- cookie that is not Secure, so the local-http escape has to drop the prefix
// rather than keep it or it signs nobody in. Only that escape drops Secure, and a developer's localhost has no
// sibling host to be written from.
func cookieName(name string, secure bool) string {
	if secure {
		return name
	}
	return strings.TrimPrefix(name, hostPrefix)
}

var errInvalidControllerSession = errors.New("invalid controller session")

type sessionResp struct {
	Principal string   `json:"principal"`
	Scopes    []string `json:"scopes"`
	CSRFToken string   `json:"csrf_token"`
	ExpiresAt int64    `json:"expires_at"`
	Team      string   `json:"team,omitempty"`
	UserID    string   `json:"user_id,omitempty"`
}

func (s *sessionResp) accountBound() bool {
	return s.UserID != "" || (s.Team != "" && s.Team != "default")
}

func accountSessionsServed(opts HandlerOptions) bool {
	return opts.ControllerURL != ""
}

func resolveDashboardSession(ctx context.Context, opts HandlerOptions, sessionID string) (*sessionResp, error) {
	sess, err := controllerResolveSession(ctx, authControllerURL(opts), sessionID)
	if err != nil {
		return nil, err
	}
	if sess.accountBound() && !accountSessionsServed(opts) {
		return nil, fmt.Errorf("%w: an account session needs a dashboard running with --controller", errInvalidControllerSession)
	}
	return sess, nil
}

func safeNext(next string) string {
	u, err := url.ParseRequestURI(next)
	if err != nil || strings.Contains(next, "#") || u.IsAbs() || u.Host != "" || u.Fragment != "" || u.Opaque != "" {
		return "/"
	}
	if !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") || strings.Contains(u.Path, `\`) {
		return "/"
	}
	return next
}

func sameOriginRequest(r *http.Request) bool {
	return sameOriginRequestOverTLS(r, requestOverTLSFrom(r.Context()))
}

// safety: without TLS evidence the process cannot know its external scheme, so
// the host comparison carries the check rather than reject the live origin.
func sameOriginRequestOverTLS(r *http.Request, overTLS bool) bool {
	raw := r.Header.Get("Origin")
	if raw == "" {
		raw = r.Referer()
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Host == "" || u.User != nil || !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	return !overTLS || strings.EqualFold(u.Scheme, "https")
}

func constantTimeEqual(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func csrfError(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "invalid CSRF token", http.StatusForbidden)
}

func sessionBackendError(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "controller session validation unavailable", http.StatusBadGateway)
}

func controllerResolveSession(ctx context.Context, controllerURL, sessionID string) (*sessionResp, error) {
	if sessionID == "" {
		return nil, errors.New("empty session id")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(controllerURL, "/")+"/api/v1/auth/session",
		nil)
	req.Header.Set("Authorization", "Session "+sessionID)
	client := &http.Client{Transport: controllerTransport, Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("%w: controller returned 401", errInvalidControllerSession)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("controller session unavailable: status %d", resp.StatusCode)
	}
	var out sessionResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode controller session: %w", err)
	}
	if out.Principal == "" || out.CSRFToken == "" || out.ExpiresAt <= 0 {
		return nil, errors.New("controller session response is missing required fields")
	}
	return &out, nil
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

func cookiesSecure(opts HandlerOptions) bool {
	return !opts.InsecureCookies
}
