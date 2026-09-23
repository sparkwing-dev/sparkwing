package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

// safety: the flow cookie is what proves the browser GitHub returns is the one
// that started connecting, and it holds the verifier only that browser has.
// Lax because GitHub returns with a cross-site top-level GET.
const githubAppFlowCookieName = hostPrefix + "sw_github_app"

const githubAppFlowTTL = 10 * time.Minute

const (
	githubAppSettingsPath = "/team/github"
	githubAppCallbackPath = "/github/app/callback"
	githubAppCompletePath = "/github/app/complete"
)

// hack: the controller's 403 for an account with no linked GitHub sign-in carries no code of its own,
// so the page matches its reason to offer the way forward.
const githubAppNoIdentityReason = "link a GitHub sign-in"

type githubAppFlow struct {
	State          string `json:"state"`
	Verifier       string `json:"verifier"`
	AuthorizeURL   string `json:"authorize_url"`
	InstallationID int64  `json:"installation_id,omitempty"`
	Code           string `json:"code,omitempty"`
}

type githubAppConnectResp struct {
	InstallURL   string `json:"install_url"`
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
	Verifier     string `json:"verifier"`
}

type githubAppInstallation struct {
	InstallationID int64  `json:"installation_id"`
	AccountLogin   string `json:"account_login"`
}

// safety: the controller call runs as the browser's session, because only an owner of the
// active team may connect and the state the controller signs names that team.
func githubAppConnectHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		secure := cookiesSecure(opts)
		principal, ok := githubAppPrincipal(w, opts, r)
		if !ok {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !validFormCSRF(r, secure) || !constantTimeEqual(r.PostForm.Get("csrf_token"), principal.csrfToken) {
			csrfError(w)
			return
		}
		var start githubAppConnectResp
		err := postControllerJSONAs(r.Context(), opts.ControllerURL, "/api/v1/team/github-app/connect",
			ratelimit.ClientIP(r, opts.TrustedProxyCIDRs), principal.sessionID,
			map[string]string{"redirect_uri": dashboardURL(r, githubAppCallbackPath)}, &start)
		if err != nil {
			renderGitHubAppRefusal(w, err)
			return
		}
		if !absoluteHTTPURL(start.InstallURL) || !absoluteHTTPURL(start.AuthorizeURL) ||
			start.State == "" || start.Verifier == "" {
			renderGitHubAppPage(w, http.StatusBadGateway, githubAppPage{
				Title:   "GitHub could not be connected",
				Message: "The controller's answer could not be used. Try again.",
			})
			return
		}
		if !setGitHubAppFlow(w, githubAppFlow{State: start.State, Verifier: start.Verifier, AuthorizeURL: start.AuthorizeURL}, secure) {
			http.Error(w, "could not start connecting GitHub", http.StatusInternalServerError)
			return
		}
		// safety: the page's form-action 'self' stops a browser following a form's redirect to
		// GitHub, and a page on this origin that moves on by itself is a navigation, not a form.
		renderGitHubAppPage(w, http.StatusOK, githubAppPage{
			Title:       "Connecting GitHub",
			Message:     "Continuing to GitHub to install the App.",
			Refresh:     start.InstallURL,
			ActionHref:  start.InstallURL,
			ActionLabel: "Continue to GitHub",
		})
	}
}

// safety: GitHub's setup return needs no session, because it only moves this browser's own flow on.
func githubAppSetupHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		secure := cookiesSecure(opts)
		query := r.URL.Query()
		if query.Get("setup_action") == "request" {
			setGitHubAppFlowCookie(w, "", -1, secure)
			renderGitHubAppPage(w, http.StatusOK, githubAppPage{
				Title: "Waiting for an organization owner",
				Message: "An org owner must approve the installation. GitHub has asked them; once they approve it, " +
					"connect again from the team's GitHub settings.",
				ActionHref:  githubAppSettingsPath,
				ActionLabel: "Back to GitHub settings",
			})
			return
		}
		flow, ok := readGitHubAppFlow(r, secure)
		// hack: GitHub also sends a change made on github.com here, with no state; the
		// repositories are read from GitHub when they matter, so nothing is left to do.
		if !ok && query.Get("state") == "" && query.Get("setup_action") == "update" {
			http.Redirect(w, r, githubAppSettingsPath, http.StatusSeeOther)
			return
		}
		if !ok {
			refuseGitHubAppFlow(w, "This connection was not started in this browser. Start again.")
			return
		}
		// safety: the cookie survives a mismatch, so one forged return cannot discard a connection in flight.
		if !constantTimeEqual(query.Get("state"), flow.State) {
			refuseGitHubAppFlow(w, "This connection could not be verified. Start again.")
			return
		}
		id, err := strconv.ParseInt(query.Get("installation_id"), 10, 64)
		if err != nil || id <= 0 {
			refuseGitHubAppFlow(w, "GitHub did not say which installation to connect. Start again.")
			return
		}
		flow.InstallationID = id
		if !setGitHubAppFlow(w, flow, secure) {
			http.Error(w, "could not continue connecting GitHub", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, flow.AuthorizeURL, http.StatusSeeOther)
	}
}

// safety: the session cookie is SameSite=Strict and a browser withholds it from GitHub's return,
// so the callback keeps the code in the flow cookie and moves on from a page on this origin,
// whose next request carries the session.
func githubAppCallbackHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		secure := cookiesSecure(opts)
		query := r.URL.Query()
		if query.Get("error") != "" {
			refuseGitHubAppFlowStatus(w, http.StatusUnauthorized, "GitHub authorization was not completed.")
			return
		}
		flow, ok := readGitHubAppFlow(r, secure)
		if !ok {
			refuseGitHubAppFlow(w, "This connection was not started in this browser. Start again.")
			return
		}
		if !constantTimeEqual(query.Get("state"), flow.State) {
			refuseGitHubAppFlow(w, "This connection could not be verified. Start again.")
			return
		}
		// safety: the installation comes only from the setup return this browser's
		// flow recorded, never from the callback URL, which anyone can write.
		if flow.InstallationID <= 0 {
			refuseGitHubAppFlow(w, "GitHub did not report an installation for this connection. Start again.")
			return
		}
		code := query.Get("code")
		if code == "" {
			refuseGitHubAppFlow(w, "GitHub authorization was not completed.")
			return
		}
		flow.Code = code
		if !setGitHubAppFlow(w, flow, secure) {
			http.Error(w, "could not continue connecting GitHub", http.StatusInternalServerError)
			return
		}
		renderGitHubAppPage(w, http.StatusOK, githubAppPage{
			Title:       "Connecting GitHub",
			Message:     "Finishing the connection.",
			Refresh:     githubAppCompletePath,
			ActionHref:  githubAppCompletePath,
			ActionLabel: "Continue",
		})
	}
}

func githubAppCompleteHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		secure := cookiesSecure(opts)
		principal, ok := githubAppPrincipal(w, opts, r)
		if !ok {
			return
		}
		flow, ok := readGitHubAppFlow(r, secure)
		if !ok || flow.InstallationID <= 0 || flow.Code == "" {
			refuseGitHubAppFlow(w, "This connection was not started in this browser. Start again.")
			return
		}
		setGitHubAppFlowCookie(w, "", -1, secure)
		var bound githubAppInstallation
		err := postControllerJSONAs(r.Context(), opts.ControllerURL, "/api/v1/team/github-app/connect/complete",
			ratelimit.ClientIP(r, opts.TrustedProxyCIDRs), principal.sessionID, map[string]any{
				"state":           flow.State,
				"verifier":        flow.Verifier,
				"code":            flow.Code,
				"installation_id": flow.InstallationID,
				"redirect_uri":    dashboardURL(r, githubAppCallbackPath),
			}, &bound)
		if err != nil {
			renderGitHubAppRefusal(w, err)
			return
		}
		next := url.URL{Path: githubAppSettingsPath, RawQuery: url.Values{"connected": {bound.AccountLogin}}.Encode()}
		http.Redirect(w, r, next.String(), http.StatusSeeOther)
	}
}

func githubAppPrincipal(w http.ResponseWriter, opts HandlerOptions, r *http.Request) (*webPrincipal, bool) {
	if opts.ControllerURL == "" {
		http.Error(w, "connecting GitHub needs a dashboard running with --controller", http.StatusNotFound)
		return nil, false
	}
	principal, ok := WebPrincipalFromContext(r.Context())
	if !ok || principal.sessionID == "" {
		renderGitHubAppPage(w, http.StatusUnauthorized, githubAppPage{
			Title:       "Sign in to connect GitHub",
			Message:     "Connecting GitHub needs a signed-in team owner.",
			ActionHref:  "/login?next=" + url.QueryEscape(githubAppSettingsPath),
			ActionLabel: "Sign in",
		})
		return nil, false
	}
	return principal, true
}

func refuseGitHubAppFlow(w http.ResponseWriter, message string) {
	refuseGitHubAppFlowStatus(w, http.StatusBadRequest, message)
}

func refuseGitHubAppFlowStatus(w http.ResponseWriter, status int, message string) {
	renderGitHubAppPage(w, status, githubAppPage{
		Title:       "GitHub was not connected",
		Message:     message,
		ActionHref:  githubAppSettingsPath,
		ActionLabel: "Back to GitHub settings",
	})
}

// safety: the controller writes its refusal reasons for the user, so a refusal shows them; an outage
// shows no controller text.
func renderGitHubAppRefusal(w http.ResponseWriter, err error) {
	page := githubAppPage{ActionHref: githubAppSettingsPath, ActionLabel: "Back to GitHub settings"}
	status := http.StatusBadGateway
	var refused *controllerStatusError
	if !errors.As(err, &refused) {
		page.Title = "GitHub could not be connected"
		page.Message = "The controller could not be reached. Try again."
		renderGitHubAppPage(w, status, page)
		return
	}
	status = refused.Status
	switch {
	case refused.Status == http.StatusForbidden && strings.Contains(refused.Message, githubAppNoIdentityReason):
		page.Title = "Sign in with GitHub first"
		page.Message = "Sign in with GitHub first to prove you own this org. Sparkwing connects an installation " +
			"only for an account whose linked GitHub sign-in administers it."
		page.ActionHref = "/auth/github/start?next=" + url.QueryEscape(githubAppSettingsPath)
		page.ActionLabel = "Sign in with GitHub"
	case refused.Status == http.StatusForbidden:
		page.Title = "GitHub did not prove you own this account"
		page.Message = orDefault(refused.Message, "Only a team owner who administers the GitHub account can connect it.")
	case refused.Status == http.StatusUnauthorized:
		page.Title = "Sign in again"
		page.Message = "Your session ended before GitHub was connected."
		page.ActionHref = "/login?next=" + url.QueryEscape(githubAppSettingsPath)
		page.ActionLabel = "Sign in"
	case refused.Status == http.StatusNotFound:
		page.Title = "Installation not found"
		page.Message = orDefault(refused.Message, "GitHub reports no installation of the App by that id.")
	case refused.Status == http.StatusConflict:
		page.Title = "Connected to another team"
		page.Message = "This installation is connected to another team. Its owner disconnects it there, or the operator moves it."
	case refused.Status == http.StatusBadRequest:
		page.Title = "GitHub was not connected"
		page.Message = orDefault(refused.Message, "The controller refused the request. Start again.")
	default:
		status = http.StatusBadGateway
		page.Title = "GitHub could not be reached"
		page.Message = "GitHub could not be reached to finish connecting. Try again."
	}
	renderGitHubAppPage(w, status, page)
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func setGitHubAppFlow(w http.ResponseWriter, flow githubAppFlow, secure bool) bool {
	value, err := json.Marshal(flow)
	if err != nil {
		return false
	}
	setGitHubAppFlowCookie(w, base64.RawURLEncoding.EncodeToString(value), int(githubAppFlowTTL/time.Second), secure)
	return true
}

func setGitHubAppFlowCookie(w http.ResponseWriter, value string, maxAge int, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName(githubAppFlowCookieName, secure),
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func readGitHubAppFlow(r *http.Request, secure bool) (githubAppFlow, bool) {
	c, err := r.Cookie(cookieName(githubAppFlowCookieName, secure))
	if err != nil || c.Value == "" {
		return githubAppFlow{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return githubAppFlow{}, false
	}
	var flow githubAppFlow
	if err := json.Unmarshal(raw, &flow); err != nil || flow.State == "" || flow.Verifier == "" ||
		!absoluteHTTPURL(flow.AuthorizeURL) {
		return githubAppFlow{}, false
	}
	return flow, true
}

type githubAppPage struct {
	Title       string
	Message     string
	Refresh     string
	ActionHref  string
	ActionLabel string
}

var githubAppPageTmpl = template.Must(template.New("github-app").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  {{if .Refresh}}<meta http-equiv="refresh" content="0;url={{.Refresh}}">{{end}}
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>{{.Title}}</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif; background: #0b0e14; color: #c9d1d9; margin: 0; display: flex; min-height: 100vh; align-items: center; justify-content: center; }
    .card { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 2rem 2.5rem; width: 100%; max-width: 440px; box-sizing: border-box; }
    h1 { font-size: 1.15rem; margin: 0 0 1rem 0; font-weight: 600; }
    p { font-size: 0.9rem; line-height: 1.45; margin: 0 0 1.25rem 0; }
    a { display: inline-block; padding: 0.5rem 0.9rem; background: #238636; color: white; border-radius: 4px; text-decoration: none; font-size: 0.9rem; }
    a:hover { background: #2ea043; }
  </style>
</head>
<body>
  <main class="card">
    <h1>{{.Title}}</h1>
    <p>{{.Message}}</p>
    {{if .ActionHref}}<a href="{{.ActionHref}}">{{.ActionLabel}}</a>{{end}}
  </main>
</body>
</html>
`))

func renderGitHubAppPage(w http.ResponseWriter, status int, page githubAppPage) {
	var body bytes.Buffer
	if err := githubAppPageTmpl.Execute(&body, page); err != nil {
		http.Error(w, page.Title+": "+page.Message, status)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// safety: a client that went away mid-page starts the connection again.
	if _, err := body.WriteTo(w); err != nil {
		return
	}
}
