package controller

import (
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/originguard"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
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

	// Bundle is the dashboard's static export, rooted at its index.html the
	// way [web.BundleFS] returns it. Nil serves pages that report the bundle
	// missing; the API and sign-in pages work either way.
	Bundle fs.FS

	// Logs reads durable node logs for the dashboard. Nil reads through the
	// logs service [Server.WithLogsURL] names, forwarding each caller's own
	// credential, or the node log files under Paths when that is empty.
	Logs storage.LogStore

	// Paths locates node log files when neither Logs nor a logs service is
	// configured.
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
	browser := http.NewServeMux()
	// safety: the page reaches the API on its own origin with its session cookie or the local serve token, so no
	// credential rides this script.
	browser.Handle("GET /sparkwing-runtime.js", web.RuntimeConfig(d.Version, !d.Local))
	top := http.NewServeMux()
	if d.Local {
		browser.Handle("/", pages)
		top.Handle("/api/", api)
	} else {
		s.signInRoutes(browser)
		browser.Handle("/", s.pageGate(pages))
		// safety: a page on another site can make a visitor's browser send a write that needs no credential, which an
		// open controller would take as its operator's; no browser on another site has a reason to write here.
		top.Handle("/api/", originguard.RefuseCrossSiteWrites(s.cookieSessions(api)))
	}
	top.Handle("/", s.pageOrAPI(api, s.logged(browser)))
	return web.SecurityHeaders(d.HSTS, top)
}

func (s *Server) signInRoutes(mux *http.ServeMux) {
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
		secure := s.cookiesSecure()
		var p *Principal
		if raw := sessionCookie(r, secure); raw != "" {
			sess, err := s.resolveBrowserSession(r.Context(), raw)
			switch {
			case err == nil:
				p = sess.principal
			case refusalIsBackend(err):
				s.refuseBrowserSession(w, err, secure)
				return
			default:
				clearSessionCookies(w, secure)
			}
		}
		if p == nil && s.AuthEnabled() {
			w.Header().Set("Cache-Control", "no-store")
			query := url.Values{"next": {safeNext(r.URL.RequestURI())}}
			http.Redirect(w, r, "/login?"+query.Encode(), http.StatusSeeOther)
			return
		}
		if operatorPage(r.URL.Path) && !s.operatorSession(p) {
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

func refusalIsBackend(err error) bool {
	return errors.Is(err, store.ErrSessionBackend)
}
