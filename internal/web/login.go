package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
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

const loginHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Sparkwing sign in</title>
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif; background: #0b0e14; color: #c9d1d9; margin: 0; display: flex; min-height: 100vh; align-items: center; justify-content: center; }
    .card { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 2rem 2.5rem; width: 100%; max-width: 360px; box-sizing: border-box; }
    h1 { font-size: 1.25rem; margin: 0 0 1.5rem 0; font-weight: 600; letter-spacing: -0.01em; }
    label { display: block; margin-bottom: 0.35rem; font-size: 0.85rem; color: #8b949e; }
    input { width: 100%; padding: 0.55rem 0.75rem; background: #0d1117; border: 1px solid #30363d; border-radius: 4px; color: #c9d1d9; font-size: 0.95rem; box-sizing: border-box; margin-bottom: 1rem; font-family: inherit; }
    input:focus { outline: none; border-color: #58a6ff; }
    button { width: 100%; padding: 0.6rem; background: #238636; color: white; border: none; border-radius: 4px; font-size: 0.95rem; font-weight: 500; cursor: pointer; }
    button:hover { background: #2ea043; }
    .err { background: #5a1d1d; border: 1px solid #f85149; border-radius: 4px; padding: 0.6rem 0.8rem; font-size: 0.85rem; color: #ffa198; margin-bottom: 1rem; }
    .note { background: #0d2a4a; border: 1px solid #1f6feb; border-radius: 4px; padding: 0.6rem 0.8rem; font-size: 0.8rem; color: #a5d6ff; margin-bottom: 1rem; line-height: 1.35; }
    .footer { margin-top: 1.25rem; font-size: 0.75rem; color: #6e7681; text-align: center; }
    .idp { display: flex; align-items: center; justify-content: center; gap: 10px; width: 100%; height: 40px; padding: 0 12px; box-sizing: border-box; border-radius: 4px; font-family: Roboto, arial, sans-serif; font-size: 14px; font-weight: 500; line-height: 20px; letter-spacing: 0.25px; text-decoration: none; margin-bottom: 0.6rem; }
    .idp svg { width: 20px; height: 20px; flex: none; }
    .gsi { background: #131314; border: 1px solid #8e918f; color: #e3e3e3; }
    .gsi:hover { background: #1f1f20; }
    .gh { background: #24292f; border: 1px solid #57606a; color: #ffffff; }
    .gh:hover { background: #2f363d; }
    .or { display: flex; align-items: center; gap: 0.75rem; margin: 1.25rem 0; font-size: 0.75rem; color: #6e7681; }
    .or::before, .or::after { content: ""; flex: 1; border-top: 1px solid #30363d; }
  </style>
</head>
<body>
  {{if .Bootstrap}}
  <form class="card" method="POST" action="/login/bootstrap">
    <h1>Create first admin</h1>
    <div class="note">This is a fresh Sparkwing cluster. The first account you create here becomes the administrator. After that, additional users must be added by an admin.</div>
    {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
    <label for="username">Username</label>
    <input id="username" name="username" type="text" autocomplete="username" autofocus required>
    <label for="password">Password</label>
    <input id="password" name="password" type="password" autocomplete="new-password" minlength="8" required>
    <input type="hidden" name="next" value="{{.Next}}">
    <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
    <button type="submit">Create admin and sign in</button>
    <div class="footer">First-visit signup</div>
  </form>
  {{else}}
  <form class="card" method="POST" action="/login">
    <h1>Sparkwing</h1>
    {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
    {{if .Google}}
    <a class="idp gsi" href="/auth/google/start?next={{.Next}}">
      <svg viewBox="0 0 48 48" aria-hidden="true"><path fill="#EA4335" d="M24 9.5c3.54 0 6.71 1.22 9.21 3.6l6.85-6.85C35.9 2.38 30.47 0 24 0 14.62 0 6.51 5.38 2.56 13.22l7.98 6.19C12.43 13.72 17.74 9.5 24 9.5z"/><path fill="#4285F4" d="M46.98 24.55c0-1.57-.15-3.09-.38-4.55H24v9.02h12.94c-.58 2.96-2.26 5.48-4.78 7.18l7.73 6c4.51-4.18 7.09-10.36 7.09-17.65z"/><path fill="#FBBC05" d="M10.53 28.59c-.48-1.45-.76-2.99-.76-4.59s.27-3.14.76-4.59l-7.98-6.19C.92 16.46 0 20.12 0 24c0 3.88.92 7.54 2.56 10.78l7.97-6.19z"/><path fill="#34A853" d="M24 48c6.48 0 11.93-2.13 15.89-5.81l-7.73-6c-2.15 1.45-4.92 2.3-8.16 2.3-6.26 0-11.57-4.22-13.47-9.91l-7.98 6.19C6.51 42.62 14.62 48 24 48z"/><path fill="none" d="M0 0h48v48H0z"/></svg>
      <span>Sign in with Google</span>
    </a>
    {{end}}
    {{if .GitHub}}
    <a class="idp gh" href="/auth/github/start?next={{.Next}}">
      <svg viewBox="0 0 16 16" aria-hidden="true"><path fill="currentColor" d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"/></svg>
      <span>Sign in with GitHub</span>
    </a>
    {{end}}
    {{if or .Google .GitHub}}<div class="or">or use a password</div>{{end}}
    <label for="username">Username</label>
    <input id="username" name="username" type="text" autocomplete="username" {{if not (or .Google .GitHub)}}autofocus {{end}}required>
    <label for="password">Password</label>
    <input id="password" name="password" type="password" autocomplete="current-password" required>
    <input type="hidden" name="next" value="{{.Next}}">
    <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
    <button type="submit">Sign in</button>
  </form>
  {{end}}
</body>
</html>
`

var loginTmpl = template.Must(template.New("login").Parse(loginHTML))

type loginPageData struct {
	Error     string
	Next      string
	CSRFToken string
	Bootstrap bool
	Google    bool
	GitHub    bool
}

func loginPageHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		controllerURL := authControllerURL(opts)
		if controllerURL == "" {
			http.Error(w, "login only available with a controller session backend", http.StatusNotFound)
			return
		}
		data := loginPageData{Next: safeNext(r.URL.Query().Get("next"))}
		if c, err := r.Cookie(cookieName(sessionCookieName, cookiesSecure(opts))); err == nil && c.Value != "" {
			if _, err := resolveDashboardSession(r.Context(), opts, c.Value); err == nil {
				http.Redirect(w, r, data.Next, http.StatusSeeOther)
				return
			} else if !errors.Is(err, errInvalidControllerSession) {
				sessionBackendError(w)
				return
			}
			clearSessionCookies(w, cookiesSecure(opts))
		}
		data.Bootstrap = controllerBootstrapNeeded(r.Context(), controllerURL)
		data = withSignInProviders(r.Context(), opts, data)
		renderLoginPage(w, r, data, http.StatusOK, cookiesSecure(opts))
	}
}

func loginSubmitHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		controllerURL := authControllerURL(opts)
		if controllerURL == "" {
			http.Error(w, "login only available with a controller session backend", http.StatusNotFound)
			return
		}
		user := r.PostForm.Get("username")
		pass := r.PostForm.Get("password")
		next := safeNext(r.PostForm.Get("next"))

		sess, err := controllerLogin(r.Context(), controllerURL, user, pass, ratelimit.ClientIP(r, opts.TrustedProxyCIDRs))
		if err != nil {
			data := withSignInProviders(r.Context(), opts,
				loginPageData{Error: "Invalid username or password.", Next: next})
			renderLoginPage(w, r, data, http.StatusUnauthorized, cookiesSecure(opts))
			return
		}

		setSessionCookies(w, sess, cookiesSecure(opts))
		http.Redirect(w, r, next, http.StatusSeeOther)
	}
}

func bootstrapSubmitHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		controllerURL := authControllerURL(opts)
		if controllerURL == "" {
			http.Error(w, "login only available with a controller session backend", http.StatusNotFound)
			return
		}
		user := strings.TrimSpace(r.PostForm.Get("username"))
		pass := r.PostForm.Get("password")
		next := safeNext(r.PostForm.Get("next"))

		if err := controllerCreateFirstUser(r.Context(), controllerURL, user, pass); err != nil {
			data := loginPageData{Next: next, Bootstrap: true, Error: err.Error()}
			if strings.Contains(err.Error(), "bootstrap closed") {
				data.Bootstrap = false
				data.Error = "Bootstrap closed -- sign in with the existing admin credentials."
			}
			renderLoginPage(w, r, data, http.StatusBadRequest, cookiesSecure(opts))
			return
		}

		sess, err := controllerLogin(r.Context(), controllerURL, user, pass, ratelimit.ClientIP(r, opts.TrustedProxyCIDRs))
		if err != nil {
			data := loginPageData{
				Next:  next,
				Error: "Admin created, but auto-login failed. Sign in with the credentials you just set.",
			}
			renderLoginPage(w, r, data, http.StatusOK, cookiesSecure(opts))
			return
		}
		setSessionCookies(w, sess, cookiesSecure(opts))
		http.Redirect(w, r, next, http.StatusSeeOther)
	}
}

func logoutHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		sessionCookie, err := r.Cookie(cookieName(sessionCookieName, cookiesSecure(opts)))
		if err != nil || sessionCookie.Value == "" {
			clearSessionCookies(w, cookiesSecure(opts))
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		controllerURL := authControllerURL(opts)
		sess, err := controllerResolveSession(r.Context(), controllerURL, sessionCookie.Value)
		if err != nil {
			if errors.Is(err, errInvalidControllerSession) {
				clearSessionCookies(w, cookiesSecure(opts))
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			} else {
				sessionBackendError(w)
			}
			return
		}
		if !constantTimeEqual(r.PostForm.Get("csrf_token"), sess.CSRFToken) {
			csrfError(w)
			return
		}
		if err := controllerLogout(r.Context(), controllerURL, sessionCookie.Value); err != nil {
			http.Error(w, "controller logout failed", http.StatusBadGateway)
			return
		}
		clearSessionCookies(w, cookiesSecure(opts))
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

func csrfFormMiddleware(secure bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginRequest(r) {
			csrfError(w)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !validFormCSRF(r, secure) {
			csrfError(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type loginResp struct {
	SessionID string   `json:"session_id"`
	CSRFToken string   `json:"csrf_token"`
	Principal string   `json:"principal"`
	Scopes    []string `json:"scopes"`
	ExpiresAt int64    `json:"expires_at"`
}

type sessionResp struct {
	Principal string   `json:"principal"`
	Scopes    []string `json:"scopes"`
	CSRFToken string   `json:"csrf_token"`
	ExpiresAt int64    `json:"expires_at"`
	Team      string   `json:"team,omitempty"`
	UserID    string   `json:"user_id,omitempty"`
}

// accountBound reports a session a signed-up account holds, or one acting for
// any team but the operator's.
func (s *sessionResp) accountBound() bool {
	return s.UserID != "" || (s.Team != "" && s.Team != "default")
}

// safety: without --controller the dashboard reads the operator's own store for
// every request, so only a session that is the operator's may use it. An
// account session would read every team's runs.
func accountSessionsServed(opts HandlerOptions) bool {
	return opts.ControllerURL != ""
}

// resolveDashboardSession is [controllerResolveSession] for a session this
// dashboard will serve: an account session on a dashboard that does not
// forward reads as the session reads as invalid.
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

func controllerLogin(ctx context.Context, controllerURL, user, pass, clientIP string) (*loginResp, error) {
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(controllerURL, "/")+"/api/v1/auth/login",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// safety: without this every browser shares the web pod's controller budget, so one of them throttles all of them.
	if clientIP != "" {
		req.Header.Set("X-Forwarded-For", clientIP)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("controller login: %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out loginResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func controllerLogout(ctx context.Context, controllerURL, sessionID string) error {
	body, _ := json.Marshal(map[string]string{"session_id": sessionID})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(controllerURL, "/")+"/api/v1/auth/logout",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("controller logout: %d", resp.StatusCode)
	}
	return nil
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

func renderLoginPage(w http.ResponseWriter, r *http.Request, data loginPageData, status int, secure bool) {
	token := ""
	if cookie, err := r.Cookie(cookieName(csrfCookieName, secure)); err == nil {
		token = cookie.Value
	}
	if token == "" {
		var err error
		token, err = newCSRFToken()
		if err != nil {
			http.Error(w, "could not create login form", http.StatusInternalServerError)
			return
		}
	}
	data.CSRFToken = token
	setCSRFCookie(w, token, secure, int(12*time.Hour/time.Second))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = loginTmpl.Execute(w, data)
}

func newCSRFToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func validFormCSRF(r *http.Request, secure bool) bool {
	if !sameOriginRequest(r) {
		return false
	}
	cookie, err := r.Cookie(cookieName(csrfCookieName, secure))
	if err != nil || cookie.Value == "" {
		return false
	}
	formToken := r.PostForm.Get("csrf_token")
	if !constantTimeEqual(formToken, cookie.Value) {
		return false
	}
	return true
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

func controllerBootstrapNeeded(ctx context.Context, controllerURL string) bool {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(controllerURL, "/")+"/api/v1/auth/bootstrap-needed", nil)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Needed bool `json:"needed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	return body.Needed
}

func controllerCreateFirstUser(ctx context.Context, controllerURL, user, pass string) error {
	body, _ := json.Marshal(map[string]string{"name": user, "password": pass})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(controllerURL, "/")+"/api/v1/users",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return nil
	}
	b, _ := io.ReadAll(resp.Body)
	msg := strings.TrimSpace(string(b))
	if resp.StatusCode == http.StatusConflict {
		return errors.New("bootstrap closed")
	}
	if msg == "" {
		return fmt.Errorf("controller create user: %d", resp.StatusCode)
	}
	return fmt.Errorf("controller create user: %d: %s", resp.StatusCode, msg)
}

func controllerResolveSession(ctx context.Context, controllerURL, sessionID string) (*sessionResp, error) {
	if sessionID == "" {
		return nil, errors.New("empty session id")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(controllerURL, "/")+"/api/v1/auth/session",
		nil)
	req.Header.Set("Authorization", "Session "+sessionID)
	client := &http.Client{Timeout: 5 * time.Second}
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

func setSessionCookies(w http.ResponseWriter, sess *loginResp, secure bool) {
	const maxAge = int(30 * 24 * time.Hour / time.Second)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName(sessionCookieName, secure),
		Value:    sess.SessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	})
	setCSRFCookie(w, sess.CSRFToken, secure, maxAge)
}

func setCSRFCookie(w http.ResponseWriter, token string, secure bool, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName(csrfCookieName, secure),
		Value:    token,
		Path:     "/",
		HttpOnly: false, // safety: the native logout form reads the session-bound token without exposing the HttpOnly session id
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

func cookiesSecure(opts HandlerOptions) bool {
	return !opts.InsecureCookies
}
