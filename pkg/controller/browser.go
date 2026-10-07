package controller

import (
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Dashboard configures the browser surface a controller serves beside its
// API on the same listener: the dashboard pages, browser sign-in and the
// session cookies that authenticate the page's own API calls.
type Dashboard struct {
	// Local serves one machine's own user, who authenticates some other way
	// (the local dashboard's serve token): no sign-in pages and no session
	// cookies.
	Local bool

	// HSTS asserts that browsers reach this controller over TLS even though
	// the process serves plaintext, for a deployment that terminates TLS in
	// front of it without forwarding a trusted X-Forwarded-Proto. It emits
	// Strict-Transport-Security and makes the CSRF origin check demand an
	// https origin.
	HSTS bool

	// Version renders in the dashboard's navigation.
	Version string

	// InsecureCookies drops Secure, and with it the __Host- prefix, from the
	// session cookies so a browser keeps a session over plain HTTP. Only a
	// dashboard published without TLS needs it.
	InsecureCookies bool
}

// WithDashboard attaches the browser surface d describes to the handler
// [Server.Handler] returns. A server without one serves its API alone.
func (s *Server) WithDashboard(d Dashboard) *Server {
	s.dashboard = &d
	return s
}

func (s *Server) browserHandler(api http.Handler) http.Handler {
	router := http.NewServeMux()
	if s.dashboard.Local {
		router.Handle("/", api)
	} else {
		router.Handle("/", s.browserRoutes(api))
	}
	return web.SecurityHeaders(s.dashboard.HSTS, router)
}

func (s *Server) browserRoutes(api http.Handler) http.Handler {
	mux := http.NewServeMux()
	signIn := func(h http.Handler) http.Handler { return s.loginLimit.middleware(h) }
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.Handle("POST /login", signIn(s.formCSRF(s.handleLoginSubmit)))
	mux.Handle("POST /login/bootstrap", signIn(s.formCSRF(s.handleBootstrapSubmit)))
	mux.Handle("POST /logout", s.formCSRF(s.handleBrowserLogout))
	mux.Handle("GET /auth/{provider}/start", signIn(http.HandlerFunc(s.handleOAuthStart)))
	mux.Handle("GET /auth/{provider}/callback", signIn(http.HandlerFunc(s.handleOAuthCallback)))
	mux.Handle("POST /auth/{provider}/link", s.formCSRF(s.handleIdentityLinkStart))
	mux.HandleFunc("GET /auth/{provider}/link/complete", s.handleIdentityLinkComplete)
	mux.Handle("POST /github/app/connect", s.formCSRF(s.handleGitHubAppConnect(false)))
	mux.Handle("POST /github/app/connect/existing", s.formCSRF(s.handleGitHubAppConnect(true)))
	mux.HandleFunc("GET /github/app/setup", s.handleGitHubAppSetup)
	mux.HandleFunc("GET "+githubAppCallbackPath, s.handleGitHubAppCallback)
	mux.HandleFunc("GET "+githubAppCompletePath, s.handleGitHubAppComplete)
	mux.HandleFunc("GET "+githubAppAvailablePath, s.handleGitHubAppAvailable)
	mux.Handle("POST /github/app/select", s.formCSRF(s.handleGitHubAppSelect))
	mux.HandleFunc("GET /sparkwing-runtime.js", s.handleRuntimeConfig)
	mux.Handle("/api/", s.cookieSessions(api))
	mux.Handle("/", api)
	return mux
}

func refusalIsBackend(err error) bool {
	return errors.Is(err, store.ErrSessionBackend)
}
