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
	githubAppSettingsPath  = "/team/github"
	githubAppCallbackPath  = "/github/app/callback"
	githubAppCompletePath  = "/github/app/complete"
	githubAppAvailablePath = "/github/app/available"
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
	Existing       bool   `json:"existing,omitempty"`
	Authorization  string `json:"authorization,omitempty"`
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
func githubAppConnectHandler(opts HandlerOptions, existing bool) http.HandlerFunc {
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
			renderFlowPage(w, http.StatusBadGateway, flowPage{
				Title:   "GitHub could not be connected",
				Message: "The controller's answer could not be used. Try again.",
			})
			return
		}
		if !setGitHubAppFlow(w, githubAppFlow{State: start.State, Verifier: start.Verifier, AuthorizeURL: start.AuthorizeURL, Existing: existing}, secure) {
			http.Error(w, "could not start connecting GitHub", http.StatusInternalServerError)
			return
		}
		// safety: the page's form-action 'self' stops a browser following a form's redirect to
		// GitHub, and a page on this origin that moves on by itself is a navigation, not a form.
		destination, message := start.InstallURL, "Continuing to GitHub to install the App."
		if existing {
			destination, message = start.AuthorizeURL, "Continuing to GitHub to authorize the App."
		}
		renderFlowPage(w, http.StatusOK, flowPage{
			Title:       "Connecting GitHub",
			Message:     message,
			Refresh:     destination,
			ActionHref:  destination,
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
			renderFlowPage(w, http.StatusOK, flowPage{
				Title: "Waiting for an organization owner",
				Message: "An org owner must approve the installation. GitHub has asked them; once they approve it, " +
					"connect again from the team's GitHub settings.",
				ActionHref:  githubAppSettingsPath,
				ActionLabel: "Back to GitHub settings",
			})
			return
		}
		flow, ok := readGitHubAppFlow(r, secure)
		// safety: a cross-site redirect withholds the Strict session cookie; a page on
		// this origin makes the next navigation carry it.
		if !ok && query.Get("state") == "" && query.Get("setup_action") == "update" {
			renderFlowPage(w, http.StatusOK, flowPage{
				Title:       "Repository access updated on GitHub",
				Message:     "Returning to your team's GitHub settings.",
				Refresh:     githubAppSettingsPath + "?access_updated=1",
				ActionHref:  githubAppSettingsPath + "?access_updated=1",
				ActionLabel: "Continue to GitHub settings",
			})
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
		if !flow.Existing && flow.InstallationID <= 0 {
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
		next, message := githubAppCompletePath, "Finishing the connection."
		if flow.Existing {
			next, message = githubAppAvailablePath, "Finding installations you administer."
		}
		renderFlowPage(w, http.StatusOK, flowPage{
			Title:       "Connecting GitHub",
			Message:     message,
			Refresh:     next,
			ActionHref:  next,
			ActionLabel: "Continue",
		})
	}
}

type githubAppAvailableResp struct {
	Authorization string `json:"authorization"`
	Installations []struct {
		InstallationID     int64  `json:"installation_id"`
		AccountLogin       string `json:"account_login"`
		AccountType        string `json:"account_type"`
		ConnectedElsewhere bool   `json:"connected_elsewhere"`
	} `json:"installations"`
}

func githubAppAvailableHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		secure := cookiesSecure(opts)
		principal, ok := githubAppPrincipal(w, opts, r)
		if !ok {
			return
		}
		flow, ok := readGitHubAppFlow(r, secure)
		if !ok || !flow.Existing || flow.Code == "" || flow.Authorization != "" {
			refuseGitHubAppFlow(w, "This connection was not started in this browser. Start again.")
			return
		}
		var available githubAppAvailableResp
		err := postControllerJSONAs(r.Context(), opts.ControllerURL, "/api/v1/team/github-app/connect/available",
			ratelimit.ClientIP(r, opts.TrustedProxyCIDRs), principal.sessionID, map[string]any{
				"state": flow.State, "verifier": flow.Verifier, "code": flow.Code,
				"redirect_uri": dashboardURL(r, githubAppCallbackPath),
			}, &available)
		if err != nil {
			renderGitHubAppRefusal(w, err)
			return
		}
		flow.Code = ""
		flow.Authorization = available.Authorization
		if flow.Authorization == "" || !setGitHubAppFlow(w, flow, secure) {
			refuseGitHubAppFlow(w, "GitHub authorization could not be kept. Start again.")
			return
		}
		if len(available.Installations) == 0 {
			var body bytes.Buffer
			if err := githubAppEmptyPickerTmpl.Execute(&body, principal.csrfToken); err != nil {
				http.Error(w, "could not show installations", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if _, err := body.WriteTo(w); err != nil {
				return
			}
			return
		}
		var body bytes.Buffer
		if err := githubAppPickerTmpl.Execute(&body, struct {
			Installations any
			CSRFToken     string
		}{available.Installations, principal.csrfToken}); err != nil {
			http.Error(w, "could not show installations", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := body.WriteTo(w); err != nil {
			return
		}
	}
}

var githubAppEmptyPickerTmpl = template.Must(template.New("github-app-empty-picker").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>No existing installations</title></head><body><main><h1>No existing installations</h1><p>Install the App on GitHub to connect a team.</p><form method="post" action="/github/app/connect"><input type="hidden" name="csrf_token" value="{{.}}"><button type="submit">Connect GitHub</button></form></main></body></html>`))

var githubAppPickerTmpl = template.Must(template.New("github-app-picker").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Connect an existing installation</title></head><body><main><h1>Connect an existing installation</h1><ul>{{range .Installations}}<li>{{.AccountLogin}} ({{.AccountType}}) {{if .ConnectedElsewhere}}Connected to another team{{else}}<form method="post" action="/github/app/select"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="installation_id" value="{{.InstallationID}}"><button type="submit">Connect</button></form>{{end}}</li>{{end}}</ul><a href="/team/github">Back to GitHub settings</a></main></body></html>`))

func githubAppSelectHandler(opts HandlerOptions) http.HandlerFunc {
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
		flow, ok := readGitHubAppFlow(r, secure)
		if !ok || !flow.Existing || flow.Authorization == "" {
			refuseGitHubAppFlow(w, "This connection was not started in this browser. Start again.")
			return
		}
		id, err := strconv.ParseInt(r.PostForm.Get("installation_id"), 10, 64)
		if err != nil || id <= 0 {
			refuseGitHubAppFlow(w, "Choose an installation.")
			return
		}
		setGitHubAppFlowCookie(w, "", -1, secure)
		var bound githubAppInstallation
		err = postControllerJSONAs(r.Context(), opts.ControllerURL, "/api/v1/team/github-app/connect/select",
			ratelimit.ClientIP(r, opts.TrustedProxyCIDRs), principal.sessionID, map[string]any{
				"state": flow.State, "verifier": flow.Verifier, "authorization": flow.Authorization, "installation_id": id,
			}, &bound)
		if err != nil {
			renderGitHubAppRefusal(w, err)
			return
		}
		next := url.URL{Path: githubAppSettingsPath, RawQuery: url.Values{"connected": {bound.AccountLogin}}.Encode()}
		http.Redirect(w, r, next.String(), http.StatusSeeOther)
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
		renderFlowPage(w, http.StatusUnauthorized, flowPage{
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
	renderFlowPage(w, status, flowPage{
		Title:       "GitHub was not connected",
		Message:     message,
		ActionHref:  githubAppSettingsPath,
		ActionLabel: "Back to GitHub settings",
	})
}

// safety: the controller writes its refusal reasons for the user, so a refusal shows them; an outage
// shows no controller text.
func renderGitHubAppRefusal(w http.ResponseWriter, err error) {
	page := flowPage{ActionHref: githubAppSettingsPath, ActionLabel: "Back to GitHub settings"}
	status := http.StatusBadGateway
	var refused *controllerStatusError
	if !errors.As(err, &refused) {
		page.Title = "GitHub could not be connected"
		page.Message = "The controller could not be reached. Try again."
		renderFlowPage(w, status, page)
		return
	}
	status = refused.Status
	switch {
	case refused.Status == http.StatusForbidden && strings.Contains(refused.Message, githubAppNoIdentityReason):
		page.Title = "Link GitHub first"
		page.Message = "Link your GitHub account to your Sparkwing account first, to prove you own this org. " +
			"Sparkwing connects an installation only for an account whose linked GitHub sign-in administers it."
		page.ActionHref = signInsSettingsPath
		page.ActionLabel = "Link GitHub"
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
	renderFlowPage(w, status, page)
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
