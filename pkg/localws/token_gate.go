package localws

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// SignInPath is where a browser trades the serve token for a session
// cookie: GET SignInPath?token=<token> sets the cookie and redirects to
// the dashboard. `sparkwing serve status` prints that link.
const SignInPath = "/auth/local"

// hack: 400 days is the longest cookie lifetime browsers honor.
const sessionMaxAge = 400 * 24 * 60 * 60

// safety: loopback is not a user boundary, so every other account on this
// machine reaches the listener; the token file is readable only by the
// account that started the dashboard, which is who this gate admits.
func requireServeToken(next http.Handler, token, addr string) http.Handler {
	cookie := sessionCookieName(addr)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/version":
			next.ServeHTTP(w, r)
			return
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/webhooks/"):
			// safety: a webhook is checked against the signature its sender made.
			next.ServeHTTP(w, r)
			return
		case r.URL.Path == SignInPath:
			signIn(w, r, token, cookie)
			return
		}
		if tokenMatches(bearer(r), token) {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(cookie); err == nil && tokenMatches(c.Value, token) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.Method != http.MethodGet {
			http.Error(w, "unauthorized: send the serve-token file in the Sparkwing home as a bearer, "+
				"or open the dashboard link `sparkwing serve status` prints", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(signInPage))
	})
}

func signIn(w http.ResponseWriter, r *http.Request, token, cookie string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet || !tokenMatches(r.URL.Query().Get("token"), token) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(signInPage))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookie, Value: token, Path: "/", MaxAge: sessionMaxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func bearer(r *http.Request) string {
	scheme, rest, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(rest)
}

func tokenMatches(got, want string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// safety: browsers scope cookies by host and not by port, so a dashboard on
// another port or home names its own cookie instead of overwriting this one.
func sessionCookieName(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "sparkwing_serve"
	}
	return "sparkwing_serve_" + port
}

const signInPage = `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
	`<title>Sparkwing dashboard</title></head><body><main><h1>Open the dashboard link</h1>` +
	`<p>This dashboard answers only the account that started it. Run <code>sparkwing serve status</code> ` +
	`and open the dashboard link it prints; this browser then stays signed in.</p></main></body></html>`
