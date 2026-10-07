package controller

import (
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/web"
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
	cookies := s.cookieSessions(api)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			cookies.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	})
}
