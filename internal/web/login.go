// Login / logout handlers + session cookie helpers. Browsers POST a
// username+password form; the cookie carries a session id resolved by
// the controller's /api/v1/auth/session endpoint. Upstream proxy calls
// continue to use opts.Token (the web pod's own service token).
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	sessionCookieName = "sw_session"
	csrfCookieName    = "sw_csrf"
)

// Pure HTML + CSS (no JavaScript) so the session id never touches the
// JS runtime.
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
	Bootstrap bool // render the "create first admin" form
}

// loginPageHandler renders the login form, or a "Create first admin"
// form on a fresh cluster (users table empty per the controller).
func loginPageHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if opts.ControllerURL == "" {
			http.Error(w, "login only available in cluster mode", http.StatusNotFound)
			return
		}
		data := loginPageData{Next: r.URL.Query().Get("next")}
		if data.Next == "" {
			data.Next = "/"
		}
		// Skip the form for already-authed callers so a logged-in user
		// arriving via back-button / stale tab isn't forced to reauth.
		if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
			if _, err := controllerResolveSession(r.Context(), opts.ControllerURL, c.Value); err == nil {
				http.Redirect(w, r, data.Next, http.StatusSeeOther)
				return
			}
		}
		data.Bootstrap = controllerBootstrapNeeded(r.Context(), opts.ControllerURL)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = loginTmpl.Execute(w, data)
	}
}

// loginSubmitHandler validates creds via the controller, sets the
// session cookies, and redirects to ?next=. On failure, re-renders the
// login page with the error.
func loginSubmitHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if opts.ControllerURL == "" {
			http.Error(w, "login only available in cluster mode", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		user := r.PostForm.Get("username")
		pass := r.PostForm.Get("password")
		next := safeNext(r.PostForm.Get("next"))

		sess, err := controllerLogin(r.Context(), opts.ControllerURL, user, pass)
		if err != nil {
			data := loginPageData{Error: "Invalid username or password.", Next: next}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_ = loginTmpl.Execute(w, data)
			return
		}

		setSessionCookies(w, sess)
		http.Redirect(w, r, next, http.StatusSeeOther)
	}
}

// bootstrapSubmitHandler creates the first admin via the controller's
// unauthenticated bootstrap path, then auto-logs-in so the user lands
// on the dashboard rather than a re-rendered login page.
func bootstrapSubmitHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if opts.ControllerURL == "" {
			http.Error(w, "login only available in cluster mode", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		user := strings.TrimSpace(r.PostForm.Get("username"))
		pass := r.PostForm.Get("password")
		next := safeNext(r.PostForm.Get("next"))

		if err := controllerCreateFirstUser(r.Context(), opts.ControllerURL, user, pass); err != nil {
			data := loginPageData{Next: next, Bootstrap: true, Error: err.Error()}
			// 409 means a user already exists; fall back to the plain
			// login form on the next render.
			if strings.Contains(err.Error(), "bootstrap closed") {
				data.Bootstrap = false
				data.Error = "Bootstrap closed -- sign in with the existing admin credentials."
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_ = loginTmpl.Execute(w, data)
			return
		}

		sess, err := controllerLogin(r.Context(), opts.ControllerURL, user, pass)
		if err != nil {
			data := loginPageData{
				Next:  next,
				Error: "Admin created, but auto-login failed. Sign in with the credentials you just set.",
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_ = loginTmpl.Execute(w, data)
			return
		}
		setSessionCookies(w, sess)
		http.Redirect(w, r, next, http.StatusSeeOther)
	}
}

// logoutHandler clears cookies and asks the controller to drop the row.
func logoutHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookieName); err == nil && opts.ControllerURL != "" {
			_ = controllerLogout(r.Context(), opts.ControllerURL, c.Value)
		}
		clearSessionCookies(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
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

func controllerLogin(ctx context.Context, controllerURL, user, pass string) (*loginResp, error) {
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(controllerURL, "/")+"/api/v1/auth/login",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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
	resp.Body.Close()
	return nil
}

// safeNext sanitizes the post-login redirect to a same-origin path.
// Rejects protocol-relative URLs (//evil.com/...) that a naive
// HasPrefix(next, "/") check would let through, plus back-slash
// variants browsers occasionally normalize.
func safeNext(next string) string {
	if len(next) < 1 || next[0] != '/' {
		return "/"
	}
	if len(next) >= 2 && (next[1] == '/' || next[1] == '\\') {
		return "/"
	}
	return next
}

// controllerBootstrapNeeded returns true only on a positive "needed"
// answer; errors and non-2xx are treated as not-needed so a network
// hiccup keeps users on the familiar login form.
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

// controllerCreateFirstUser posts an unauthenticated create-user. The
// controller accepts it iff the users table is empty; otherwise it
// returns 409 ("bootstrap closed") which bubbles up as an error here.
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("controller session: %d", resp.StatusCode)
	}
	var out sessionResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func setSessionCookies(w http.ResponseWriter, sess *loginResp) {
	// MaxAge mirrors the controller's session TTL (12h).
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.SessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   cookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(12 * time.Hour / time.Second),
	})
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    sess.CSRFToken,
		Path:     "/",
		HttpOnly: false, // the SPA reads this and echoes it in X-Sparkwing-Csrf
		Secure:   cookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(12 * time.Hour / time.Second),
	})
}

func clearSessionCookies(w http.ResponseWriter) {
	for _, name := range []string{sessionCookieName, csrfCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Secure:   cookieSecure,
			HttpOnly: name == sessionCookieName,
		})
	}
}

// cookieSecure defaults to true; flipped to false in laptop-local dev
// (plain http on 127.0.0.1) via SPARKWING_WEB_INSECURE_COOKIES.
var cookieSecure = func() bool {
	v := os.Getenv("SPARKWING_WEB_INSECURE_COOKIES")
	return !(v == "1" || strings.EqualFold(v, "true"))
}()
