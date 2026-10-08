package controller

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// safety: the flow cookie is what proves the browser GitHub returns is the one that started connecting, and it holds
// the verifier only that browser has.
const githubAppFlowCookieName = hostPrefix + "sw_github_app"

const (
	githubAppSettingsPath  = "/team/github"
	githubAppCallbackPath  = "/github/app/callback"
	githubAppCompletePath  = "/github/app/complete"
	githubAppAvailablePath = "/github/app/available"
)

type githubAppFlow struct {
	State          string `json:"state"`
	Verifier       string `json:"verifier"`
	AuthorizeURL   string `json:"authorize_url"`
	InstallationID int64  `json:"installation_id,omitempty"`
	Code           string `json:"code,omitempty"`
	Existing       bool   `json:"existing,omitempty"`
	Authorization  string `json:"authorization,omitempty"`
}

func (f githubAppFlow) proof() oauthFlowProof {
	return oauthFlowProof{State: f.State, Verifier: f.Verifier, Code: f.Code}
}

func (s *Server) readGitHubAppFlow(r *http.Request) (githubAppFlow, bool) {
	var flow githubAppFlow
	if !readFlowCookie(r, githubAppFlowCookieName, s.cookiesSecure(), &flow) ||
		flow.State == "" || flow.Verifier == "" || !absoluteHTTPURL(flow.AuthorizeURL) {
		return githubAppFlow{}, false
	}
	return flow, true
}

func (s *Server) setGitHubAppFlow(w http.ResponseWriter, flow githubAppFlow) bool {
	return setFlowCookie(w, githubAppFlowCookieName, flow, s.cookiesSecure())
}

func (s *Server) connectingBrowser(w http.ResponseWriter, r *http.Request) (*browserSession, bool) {
	sess, err := s.signedInBrowser(r.Context(), r)
	if err == nil {
		return sess, true
	}
	if refusalIsBackend(err) {
		s.refuseBrowserSession(w, err, s.cookiesSecure())
		return nil, false
	}
	renderFlowPage(w, http.StatusUnauthorized, flowPage{
		Title:       "Sign in to connect GitHub",
		Message:     "Connecting GitHub needs a signed-in team owner.",
		ActionHref:  "/login?next=" + url.QueryEscape(githubAppSettingsPath),
		ActionLabel: "Sign in",
	})
	return nil, false
}

func (s *Server) handleGitHubAppConnect(existing bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.connectingBrowser(w, r)
		if !ok {
			return
		}
		if !constantTimeEqual(r.PostForm.Get("csrf_token"), sess.csrfToken) {
			csrfError(w)
			return
		}
		grant, err := s.githubAppConnectBegin(r.Context(), sess.principal, browserURL(r, githubAppCallbackPath))
		if err != nil {
			s.renderGitHubAppRefusal(w, err)
			return
		}
		if !s.setGitHubAppFlow(w, githubAppFlow{State: grant.State, Verifier: grant.Verifier, AuthorizeURL: grant.AuthorizeURL, Existing: existing}) {
			http.Error(w, "could not start connecting GitHub", http.StatusInternalServerError)
			return
		}
		// safety: the page's form-action 'self' stops a browser following a form's redirect to
		// GitHub, and a page on this origin that moves on by itself is a navigation, not a form.
		destination, message := grant.InstallURL, "Continuing to GitHub to install the App."
		if existing {
			destination, message = grant.AuthorizeURL, "Continuing to GitHub to authorize the App."
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
func (s *Server) handleGitHubAppSetup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	if query.Get("setup_action") == "request" {
		clearFlowCookie(w, githubAppFlowCookieName, s.cookiesSecure())
		renderFlowPage(w, http.StatusOK, flowPage{
			Title: "Waiting for an organization owner",
			Message: "An org owner must approve the installation. GitHub has asked them; once they approve it, " +
				"connect again from the team's GitHub settings.",
			ActionHref:  githubAppSettingsPath,
			ActionLabel: "Back to GitHub settings",
		})
		return
	}
	flow, ok := s.readGitHubAppFlow(r)
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
		refuseGitHubAppFlow(w, http.StatusBadRequest, "This connection was not started in this browser. Start again.")
		return
	}
	// safety: the cookie survives a mismatch, so one forged return cannot discard a connection in flight.
	if !constantTimeEqual(query.Get("state"), flow.State) {
		refuseGitHubAppFlow(w, http.StatusBadRequest, "This connection could not be verified. Start again.")
		return
	}
	id, err := strconv.ParseInt(query.Get("installation_id"), 10, 64)
	if err != nil || id <= 0 {
		refuseGitHubAppFlow(w, http.StatusBadRequest, "GitHub did not say which installation to connect. Start again.")
		return
	}
	flow.InstallationID = id
	if !s.setGitHubAppFlow(w, flow) {
		http.Error(w, "could not continue connecting GitHub", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, flow.AuthorizeURL, http.StatusSeeOther)
}

// safety: the callback keeps GitHub's code in the flow cookie and resumes on this origin before using the session
// to connect the installation.
func (s *Server) handleGitHubAppCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	if query.Get("error") != "" {
		refuseGitHubAppFlow(w, http.StatusUnauthorized, "GitHub authorization was not completed.")
		return
	}
	flow, ok := s.readGitHubAppFlow(r)
	if !ok {
		refuseGitHubAppFlow(w, http.StatusBadRequest, "This connection was not started in this browser. Start again.")
		return
	}
	if !constantTimeEqual(query.Get("state"), flow.State) {
		refuseGitHubAppFlow(w, http.StatusBadRequest, "This connection could not be verified. Start again.")
		return
	}
	// safety: the installation comes only from the setup return this browser's flow recorded, never from the
	// callback URL, which anyone can write.
	if !flow.Existing && flow.InstallationID <= 0 {
		refuseGitHubAppFlow(w, http.StatusBadRequest, "GitHub did not report an installation for this connection. Start again.")
		return
	}
	code := query.Get("code")
	if code == "" {
		refuseGitHubAppFlow(w, http.StatusBadRequest, "GitHub authorization was not completed.")
		return
	}
	flow.Code = code
	if !s.setGitHubAppFlow(w, flow) {
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

var githubAppEmptyPickerTmpl = template.Must(template.New("github-app-empty-picker").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>No existing installations</title></head><body><main><h1>No existing installations</h1><p>Install the App on GitHub to connect a team.</p><form method="post" action="/github/app/connect"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><button type="submit">Connect GitHub</button></form></main></body></html>`))

var githubAppPickerTmpl = template.Must(template.New("github-app-picker").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Connect an existing installation</title></head><body><main><h1>Connect an existing installation</h1><ul>{{range .Installations}}<li>{{.AccountLogin}} ({{.AccountType}}) <form method="post" action="/github/app/select"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="installation_id" value="{{.InstallationID}}"><button type="submit">Connect</button></form></li>{{end}}</ul><a href="/team/github">Back to GitHub settings</a></main></body></html>`))

func (s *Server) handleGitHubAppAvailable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	sess, ok := s.connectingBrowser(w, r)
	if !ok {
		return
	}
	flow, ok := s.readGitHubAppFlow(r)
	if !ok || !flow.Existing || flow.Code == "" || flow.Authorization != "" {
		refuseGitHubAppFlow(w, http.StatusBadRequest, "This connection was not started in this browser. Start again.")
		return
	}
	available, err := s.githubAppAvailable(r.Context(), sess.principal, flow.proof(), browserURL(r, githubAppCallbackPath))
	if err != nil {
		s.renderGitHubAppRefusal(w, err)
		return
	}
	flow.Code = ""
	flow.Authorization = available.Authorization
	if flow.Authorization == "" || !s.setGitHubAppFlow(w, flow) {
		refuseGitHubAppFlow(w, http.StatusBadRequest, "GitHub authorization could not be kept. Start again.")
		return
	}
	page := struct {
		Installations []githubAppAvailableInstallation
		CSRFToken     string
	}{available.Installations, sess.csrfToken}
	if len(available.Installations) == 0 {
		renderPage(w, http.StatusOK, githubAppEmptyPickerTmpl, page)
		return
	}
	renderPage(w, http.StatusOK, githubAppPickerTmpl, page)
}

func (s *Server) handleGitHubAppSelect(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.connectingBrowser(w, r)
	if !ok {
		return
	}
	if !constantTimeEqual(r.PostForm.Get("csrf_token"), sess.csrfToken) {
		csrfError(w)
		return
	}
	flow, ok := s.readGitHubAppFlow(r)
	if !ok || !flow.Existing || flow.Authorization == "" {
		refuseGitHubAppFlow(w, http.StatusBadRequest, "This connection was not started in this browser. Start again.")
		return
	}
	id, err := strconv.ParseInt(r.PostForm.Get("installation_id"), 10, 64)
	if err != nil || id <= 0 {
		refuseGitHubAppFlow(w, http.StatusBadRequest, "Choose an installation.")
		return
	}
	clearFlowCookie(w, githubAppFlowCookieName, s.cookiesSecure())
	bound, err := s.githubAppSelect(r.Context(), sess.principal, flow.proof(), flow.Authorization, id)
	if err != nil {
		s.renderGitHubAppRefusal(w, err)
		return
	}
	connected := url.URL{Path: githubAppSettingsPath, RawQuery: url.Values{"connected": {bound.AccountLogin}}.Encode()}
	http.Redirect(w, r, connected.String(), http.StatusSeeOther)
}

func (s *Server) handleGitHubAppComplete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	sess, ok := s.connectingBrowser(w, r)
	if !ok {
		return
	}
	flow, ok := s.readGitHubAppFlow(r)
	if !ok || flow.InstallationID <= 0 || flow.Code == "" {
		refuseGitHubAppFlow(w, http.StatusBadRequest, "This connection was not started in this browser. Start again.")
		return
	}
	clearFlowCookie(w, githubAppFlowCookieName, s.cookiesSecure())
	bound, err := s.githubAppConnectFinish(r.Context(), sess.principal, flow.proof(), flow.InstallationID,
		browserURL(r, githubAppCallbackPath))
	if err != nil {
		s.renderGitHubAppRefusal(w, err)
		return
	}
	noteAuditWrite(r.Context())
	connected := url.URL{Path: githubAppSettingsPath, RawQuery: url.Values{"connected": {bound.AccountLogin}}.Encode()}
	http.Redirect(w, r, connected.String(), http.StatusSeeOther)
}

func refuseGitHubAppFlow(w http.ResponseWriter, status int, message string) {
	renderFlowPage(w, status, flowPage{
		Title:       "GitHub was not connected",
		Message:     message,
		ActionHref:  githubAppSettingsPath,
		ActionLabel: "Back to GitHub settings",
	})
}

// safety: a refusal's message is written for the person in the flow, so the page shows it; a fault shows no
// internal text.
func (s *Server) renderGitHubAppRefusal(w http.ResponseWriter, err error) {
	page := flowPage{ActionHref: githubAppSettingsPath, ActionLabel: "Back to GitHub settings"}
	refused, ok := refusalOf(err)
	if !ok {
		s.logger.Error("github app connect", "error", err.Error())
		page.Title = "GitHub could not be connected"
		page.Message = "Connecting failed on this server. Try again."
		renderFlowPage(w, http.StatusInternalServerError, page)
		return
	}
	status := refused.status
	switch {
	case refused.code == githubIdentityMissing:
		page.Title = "Link GitHub first"
		page.Message = "Link your GitHub account to your Sparkwing account first, to prove you own this org. " +
			"Sparkwing connects an installation only for an account whose linked GitHub sign-in administers it."
		page.ActionHref = signInsSettingsPath
		page.ActionLabel = "Link GitHub"
	case refused.status == http.StatusForbidden:
		page.Title = "GitHub did not prove you own this account"
		page.Message = orDefault(refused.message, "Only a team owner who administers the GitHub account can connect it.")
	case refused.status == http.StatusUnauthorized:
		page.Title = "Sign in again"
		page.Message = "Your session ended before GitHub was connected."
		page.ActionHref = "/login?next=" + url.QueryEscape(githubAppSettingsPath)
		page.ActionLabel = "Sign in"
	case refused.status == http.StatusNotFound:
		page.Title = "Installation not found"
		page.Message = orDefault(refused.message, "GitHub reports no installation of the App by that id.")
	case refused.status == http.StatusConflict:
		page.Title = "Installation unavailable"
		page.Message = "This installation is unavailable to this team. Contact the operator if you expected to connect it."
	case refused.status == http.StatusBadRequest:
		page.Title = "GitHub was not connected"
		page.Message = orDefault(refused.message, "The request was refused. Start again.")
	default:
		status = http.StatusBadGateway
		page.Title = "GitHub could not be reached"
		page.Message = "GitHub could not be reached to finish connecting. Try again."
	}
	renderFlowPage(w, status, page)
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
