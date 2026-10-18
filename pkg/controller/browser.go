package controller

import (
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Dashboard configures the browser surface a controller serves beside its
// API on the same listener: the dashboard pages, browser sign-in and the
// session cookies that authenticate the page's own API calls.
type Dashboard struct {
	// Local uses verified peer transport without browser sign-in.
	Local bool

	// HSTS asserts that browsers reach this controller over TLS even though
	// the process serves plaintext, for a deployment that terminates TLS in
	// front of it without forwarding a trusted X-Forwarded-Proto. It emits
	// Strict-Transport-Security and makes the CSRF origin check demand an
	// https origin.
	HSTS bool

	// Version renders in the dashboard's navigation.
	Version string

	// Bundle is the dashboard's static export, rooted at its index.html the
	// way [web.BundleFS] returns it. Nil serves pages that report the bundle
	// missing; the API and sign-in pages work either way.
	Bundle fs.FS

	// Paths locates a local dashboard's run directories.
	Paths paths.Paths

	// Capabilities names the mode, storage and features GET
	// /api/v1/capabilities reports beside the controller's identity fields.
	Capabilities backend.Capabilities

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
	d := s.dashboard
	pages := web.Pages(d.Bundle)
	browser := newRegisteredMux()
	browser.Handle("GET /sparkwing-runtime.js", web.RuntimeConfig(d.Version, !d.Local))
	top := http.NewServeMux()
	if d.Local {
		browser.Handle("/", pages)
		top.Handle("/api/", api)
	} else {
		s.signInRoutes(browser)
		browser.Handle("/", s.pageGate(pages))
		top.Handle("/api/", api)
	}
	top.Handle("/", s.pageOrAPI(api, s.logged(browser.ServeMux)))
	return web.SecurityHeaders(d.HSTS, top)
}

func (s *Server) signInRoutes(mux *registeredMux) {
	s.registerPolicyEndpoints(mux, s.loginEndpoints())
	s.registerPolicyEndpoints(mux, s.browserSessionEndpoints())
	s.registerPolicyEndpoints(mux, s.oauthEndpoints())
	s.registerPolicyEndpoints(mux, s.identityLinkEndpoints())
	s.registerPolicyEndpoints(mux, s.githubBrowserEndpoints())
}

// safety: the browser's own routes write the same request log, audit record and X-Request-Id the API does, so a
// refused sign-in or a logout leaves the trail docs/security.md promises; the API chain logs itself.
func (s *Server) logged(mux *http.ServeMux) http.Handler {
	return otelutil.WrapHandler("sparkwing-controller",
		withRequestLog(mux, s.logger, muxRouteLabeler(mux), ratelimit.ClientIP))
}

// safety: the API owns every route it registers outside /api/ (webhooks, metrics, OIDC discovery and the internal
// service routes), so only a path it does not know is a dashboard page.
func (s *Server) pageOrAPI(api, pages http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.RegisteredRoute(r) {
			api.ServeHTTP(w, r)
			return
		}
		pages.ServeHTTP(w, r)
	})
}

// safety: a controller that authenticates its API shows its pages only to a signed-in browser, and the bundle's
// favicons and immutable build assets, which carry no run data, to anyone. The operator console's page goes only to
// a listed operator's own session.
func (s *Server) pageGate(pages http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if web.PublicAsset(r, s.dashboard.Bundle) {
			pages.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writePolicyError(w, &apierr.Error{Code: apierr.MethodNotAllowed, Message: "page method is not registered"})
			return
		}
		secure := s.cookiesSecure()
		var session *store.Session
		if raw := sessionCookie(r, secure); raw != "" {
			sess, err := s.readSessionFlow(r)
			switch {
			case err == nil:
				session = sess
			case sessionFlowUnavailable(err):
				writePolicyError(w, err)
				return
			default:
				clearSessionCookies(w, secure)
			}
		}
		if session == nil {
			w.Header().Set("Cache-Control", "no-store")
			query := url.Values{"next": {safeNext(r.URL.RequestURI())}}
			http.Redirect(w, r, "/login?"+query.Encode(), http.StatusSeeOther)
			return
		}
		if operatorPage(r.URL.Path) && (session == nil || session.CapabilityPrefix != "" || !s.operatorAccount(session.AccountID)) {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "the operator console needs the operator's own signed-in account", http.StatusForbidden)
			return
		}
		pages.ServeHTTP(w, r)
	})
}

func operatorPage(p string) bool {
	return p == "/operator" || strings.HasPrefix(p, "/operator/") || strings.HasPrefix(p, "/operator.")
}

func sessionFlowUnavailable(err error) bool {
	var failure *apierr.Error
	return errors.As(err, &failure) && failure.Code != apierr.Unauthenticated
}
