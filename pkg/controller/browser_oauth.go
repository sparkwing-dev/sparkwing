package controller

import (
	"html/template"
	"net/http"
	"net/url"
)

// safety: the flow cookie is what proves the browser finishing a sign-in is the one that started it; without it a
// callback URL someone else arranged would sign this browser in as them.
const oauthFlowCookieName = hostPrefix + "sw_oauth"

const oauthFlowLink = "link"

const signInsSettingsPath = "/account/sign-ins"

type oauthFlow struct {
	Provider string `json:"provider"`
	State    string `json:"state"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
	Mode     string `json:"mode,omitempty"`
	Code     string `json:"code,omitempty"`
}

func (s *Server) readOAuthFlow(r *http.Request) (oauthFlow, bool) {
	var flow oauthFlow
	if !readFlowCookie(r, oauthFlowCookieName, s.cookiesSecure(), &flow) ||
		flow.Provider == "" || flow.State == "" || flow.Verifier == "" {
		return oauthFlow{}, false
	}
	return flow, true
}

func oauthCallbackPath(provider string) string {
	return "/auth/" + provider + "/callback"
}

func (s *Server) refuseOAuth(w http.ResponseWriter, r *http.Request, status int, message string) {
	s.renderLoginPage(w, r, s.withSignInProviders(loginPageData{Next: "/", Error: message}), status)
}

func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	name := r.PathValue("provider")
	label, known := providerLabels[name]
	if !known {
		http.NotFound(w, r)
		return
	}
	next := safeNext(r.URL.Query().Get("next"))
	grant, err := s.oauthBegin(name, browserURL(r, oauthCallbackPath(name)))
	if err != nil {
		status := http.StatusBadGateway
		if refused, ok := refusalOf(err); ok {
			status = refused.status
		} else {
			s.logger.Error("oauth start", "provider", name, "error", err.Error())
		}
		s.renderLoginPage(w, r, s.withSignInProviders(loginPageData{
			Next: next, Error: label + " sign-in is unavailable right now.",
		}), status)
		return
	}
	if !setFlowCookie(w, oauthFlowCookieName, oauthFlow{Provider: name, State: grant.State, Verifier: grant.Verifier, Next: next}, s.cookiesSecure()) {
		http.Error(w, "could not start sign-in", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, grant.AuthorizeURL, http.StatusSeeOther)
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	name := r.PathValue("provider")
	label, known := providerLabels[name]
	if !known {
		http.NotFound(w, r)
		return
	}
	query := r.URL.Query()
	flow, started := s.readOAuthFlow(r)
	if started && flow.Mode == oauthFlowLink && flow.Provider == name {
		s.identityLinkCallback(w, r, name, flow)
		return
	}
	if query.Get("error") != "" {
		s.refuseOAuth(w, r, http.StatusUnauthorized, label+" sign-in was not completed.")
		return
	}
	if !started {
		s.refuseOAuth(w, r, http.StatusBadRequest, "This sign-in was not started in this browser. Start again.")
		return
	}
	// safety: the cookie survives a mismatch, so one forged callback cannot discard a sign-in this browser has in
	// flight. The provider is part of the match, so a code from one provider never meets another's verifier.
	if !constantTimeEqual(query.Get("state"), flow.State) || flow.Provider != name {
		s.refuseOAuth(w, r, http.StatusBadRequest, "This sign-in could not be verified. Start again.")
		return
	}
	clearFlowCookie(w, oauthFlowCookieName, s.cookiesSecure())
	code := query.Get("code")
	if code == "" {
		s.refuseOAuth(w, r, http.StatusBadRequest, label+" sign-in was not completed.")
		return
	}
	sess, raw, err := s.oauthSignIn(r.Context(), name, code, flow.Verifier, browserURL(r, oauthCallbackPath(name)))
	if err != nil {
		refused, ok := refusalOf(err)
		if !ok || refused.status >= http.StatusInternalServerError {
			if !ok {
				s.logger.Error("oauth sign-in", "provider", name, "error", err.Error())
			}
			s.refuseOAuth(w, r, http.StatusBadGateway, label+" sign-in could not be completed.")
			return
		}
		s.refuseOAuth(w, r, refused.status, refused.message)
		return
	}
	s.endPriorSession(r, raw)
	setSessionCookies(w, raw, sess.CSRFToken, s.cookiesSecure())
	renderPage(w, http.StatusOK, signedInTmpl, safeNext(flow.Next))
}

// safety: the provider returns through this origin before the browser follows the vetted next path, so the session
// never rides an untrusted redirect target.
var signedInTmpl = template.Must(template.New("signed-in").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta http-equiv="refresh" content="0;url={{.}}">
  <title>Signed in</title>
  <style>body { font-family: system-ui, sans-serif; background: #0b0e14; color: #c9d1d9; display: flex; min-height: 100vh; align-items: center; justify-content: center; margin: 0; } a { color: #58a6ff; }</style>
</head>
<body><p>Signed in. <a href="{{.}}">Continue</a></p></body>
</html>
`))

// safety: the settings page shows fixed text for each of these codes and nothing the URL carries, so a crafted
// link cannot put words on the page.
var identityLinkRefusals = map[string]bool{
	"identity_linked_elsewhere": true,
	"identity_already_linked":   true,
	"provider_already_linked":   true,
	"reauth_required":           true,
	"rate_limited":              true,
	"link_state_invalid":        true,
	"provider_unverified":       true,
	"provider_rejected":         true,
	"provider_unreachable":      true,
}

const identityLinkFailed = "link_failed"

func (s *Server) refuseIdentityLink(w http.ResponseWriter, r *http.Request, provider string, err error) {
	code := identityLinkFailed
	if refused, ok := refusalOf(err); ok && identityLinkRefusals[refused.code] {
		code = refused.code
	} else if !ok {
		s.logger.Error("identity link", "provider", provider, "error", err.Error())
	}
	redirectToSignIns(w, r, url.Values{"refused": {code}, "provider": {provider}})
}

func redirectToSignIns(w http.ResponseWriter, r *http.Request, query url.Values) {
	http.Redirect(w, r, (&url.URL{Path: signInsSettingsPath, RawQuery: query.Encode()}).String(), http.StatusSeeOther)
}

func (s *Server) linkingBrowser(w http.ResponseWriter, r *http.Request) (*browserSession, bool) {
	sess, err := s.signedInBrowser(r.Context(), r)
	if err == nil {
		return sess, true
	}
	if refusalIsBackend(err) {
		s.refuseBrowserSession(w, err, s.cookiesSecure())
		return nil, false
	}
	http.Redirect(w, r, "/login?next="+url.QueryEscape(signInsSettingsPath), http.StatusSeeOther)
	return nil, false
}

func (s *Server) handleIdentityLinkStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	label, known := providerLabels[name]
	if !known {
		http.NotFound(w, r)
		return
	}
	sess, ok := s.linkingBrowser(w, r)
	if !ok {
		return
	}
	if !constantTimeEqual(r.PostForm.Get("csrf_token"), sess.csrfToken) {
		csrfError(w)
		return
	}
	grant, err := s.identityLinkBegin(r.Context(), sess.principal, name, browserURL(r, oauthCallbackPath(name)))
	if err != nil {
		s.refuseIdentityLink(w, r, name, err)
		return
	}
	if !setFlowCookie(w, oauthFlowCookieName, oauthFlow{Provider: name, State: grant.State, Verifier: grant.Verifier, Mode: oauthFlowLink}, s.cookiesSecure()) {
		http.Error(w, "could not start linking", http.StatusInternalServerError)
		return
	}
	// safety: the page's form-action 'self' stops a browser following a form's redirect to the provider, and a page
	// on this origin that moves on by itself is a navigation.
	renderFlowPage(w, http.StatusOK, flowPage{
		Title:       "Linking " + label,
		Message:     "Continuing to " + label + " to confirm the account.",
		Refresh:     grant.AuthorizeURL,
		ActionHref:  grant.AuthorizeURL,
		ActionLabel: "Continue to " + label,
	})
}

// safety: the callback keeps the provider code in the flow cookie and resumes on this origin before checking the
// account-link state against the session.
func (s *Server) identityLinkCallback(w http.ResponseWriter, r *http.Request, name string, flow oauthFlow) {
	label := providerLabels[name]
	query := r.URL.Query()
	if query.Get("error") != "" {
		clearFlowCookie(w, oauthFlowCookieName, s.cookiesSecure())
		redirectToSignIns(w, r, url.Values{"refused": {"provider_rejected"}, "provider": {name}})
		return
	}
	// safety: the cookie survives a mismatch, so one forged return cannot discard a link in flight.
	if !constantTimeEqual(query.Get("state"), flow.State) || query.Get("code") == "" {
		renderFlowPage(w, http.StatusBadRequest, flowPage{
			Title:       label + " was not linked",
			Message:     "This link could not be verified. Start again.",
			ActionHref:  signInsSettingsPath,
			ActionLabel: "Back to linked sign-ins",
		})
		return
	}
	flow.Code = query.Get("code")
	if !setFlowCookie(w, oauthFlowCookieName, flow, s.cookiesSecure()) {
		http.Error(w, "could not continue linking", http.StatusInternalServerError)
		return
	}
	next := "/auth/" + name + "/link/complete"
	renderFlowPage(w, http.StatusOK, flowPage{
		Title:       "Linking " + label,
		Message:     "Finishing the link.",
		Refresh:     next,
		ActionHref:  next,
		ActionLabel: "Continue",
	})
}

func (s *Server) handleIdentityLinkComplete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	name := r.PathValue("provider")
	if _, known := providerLabels[name]; !known {
		http.NotFound(w, r)
		return
	}
	sess, ok := s.linkingBrowser(w, r)
	if !ok {
		return
	}
	flow, started := s.readOAuthFlow(r)
	if !started || flow.Mode != oauthFlowLink || flow.Provider != name || flow.Code == "" {
		redirectToSignIns(w, r, url.Values{"refused": {"link_state_invalid"}, "provider": {name}})
		return
	}
	clearFlowCookie(w, oauthFlowCookieName, s.cookiesSecure())
	err := s.identityLinkFinish(r.Context(), sess.principal, name,
		oauthFlowProof{State: flow.State, Verifier: flow.Verifier, Code: flow.Code}, browserURL(r, oauthCallbackPath(name)))
	if err != nil {
		s.refuseIdentityLink(w, r, name, err)
		return
	}
	redirectToSignIns(w, r, url.Values{"linked": {name}})
}
