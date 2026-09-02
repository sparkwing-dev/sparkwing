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

const (
	sessionCookieName = "sw_session"
	csrfCookieName    = "sw_csrf"
	csrfHeaderName    = "X-CSRF-Token"
)

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
    <label for="username">Username</label>
    <input id="username" name="username" type="text" autocomplete="username" autofocus required>
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
		if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
			if _, err := controllerResolveSession(r.Context(), controllerURL, c.Value); err == nil {
				http.Redirect(w, r, data.Next, http.StatusSeeOther)
				return
			} else if !errors.Is(err, errInvalidControllerSession) {
				sessionBackendError(w)
				return
			}
			clearSessionCookies(w, cookiesSecure(opts))
		}
		data.Bootstrap = controllerBootstrapNeeded(r.Context(), controllerURL)
		renderLoginPage(w, data, http.StatusOK, cookiesSecure(opts))
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
			data := loginPageData{Error: "Invalid username or password.", Next: next}
			renderLoginPage(w, data, http.StatusUnauthorized, cookiesSecure(opts))
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
			renderLoginPage(w, data, http.StatusBadRequest, cookiesSecure(opts))
			return
		}

		sess, err := controllerLogin(r.Context(), controllerURL, user, pass, ratelimit.ClientIP(r, opts.TrustedProxyCIDRs))
		if err != nil {
			data := loginPageData{
				Next:  next,
				Error: "Admin created, but auto-login failed. Sign in with the credentials you just set.",
			}
			renderLoginPage(w, data, http.StatusOK, cookiesSecure(opts))
			return
		}
		setSessionCookies(w, sess, cookiesSecure(opts))
		http.Redirect(w, r, next, http.StatusSeeOther)
	}
}

func logoutHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		sessionCookie, err := r.Cookie(sessionCookieName)
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

func csrfFormMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginRequest(r) {
			csrfError(w)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !validFormCSRF(r) {
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

func renderLoginPage(w http.ResponseWriter, data loginPageData, status int, secure bool) {
	token, err := newCSRFToken()
	if err != nil {
		http.Error(w, "could not create login form", http.StatusInternalServerError)
		return
	}
	data.CSRFToken = token
	setCSRFCookie(w, token, secure)
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

func validFormCSRF(r *http.Request) bool {
	if !sameOriginRequest(r) {
		return false
	}
	cookie, err := r.Cookie(csrfCookieName)
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
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.SessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(12 * time.Hour / time.Second),
	})
	setCSRFCookie(w, sess.CSRFToken, secure)
}

func setCSRFCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false, // safety: the native logout form reads the session-bound token without exposing the HttpOnly session id
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(12 * time.Hour / time.Second),
	})
}

func clearSessionCookies(w http.ResponseWriter, secure bool) {
	for _, name := range []string{sessionCookieName, csrfCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
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
