package controller

import (
	"html/template"
	"net/http"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
	if len(r.CookiesNamed(cookieName(oauthFlowCookieName, s.cookiesSecure()))) != 1 ||
		!readFlowCookie(r, oauthFlowCookieName, s.cookiesSecure(), &flow) ||
		flow.Provider == "" || flow.State == "" || flow.Verifier == "" ||
		flow.Mode != "" && flow.Mode != oauthFlowLink {
		return oauthFlow{}, false
	}
	return flow, true
}

func oauthCallbackPath(provider string) string {
	return "/auth/" + provider + "/callback"
}

func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	proof, ok := r.Context().Value(oauthProofKey{}).(oauthProof)
	if !ok || proof.grant.AuthorizeURL == "" {
		return missingEndpointPolicy()
	}
	if !setFlowCookie(w, oauthFlowCookieName, proof.flow, s.cookiesSecure()) {
		return &apierr.Error{Code: apierr.Internal, Message: "could not start sign-in"}
	}
	http.Redirect(w, r, proof.grant.AuthorizeURL, http.StatusSeeOther)
	return nil
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	proof, ok := r.Context().Value(oauthProofKey{}).(oauthProof)
	if !ok {
		return missingEndpointPolicy()
	}
	if proof.flow.Mode == oauthFlowLink {
		return s.continueIdentityLink(w, r, proof.flow)
	}
	if proof.session == nil || proof.raw == "" {
		return missingEndpointPolicy()
	}
	clearFlowCookie(w, oauthFlowCookieName, s.cookiesSecure())
	noteAuditWrite(r.Context())
	s.endPriorSession(r, proof.raw)
	setSessionCookies(w, proof.raw, proof.session.CSRFToken, s.cookiesSecure())
	renderPage(w, http.StatusOK, signedInTmpl, safeNext(proof.flow.Next))
	return nil
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

func (s *Server) continueIdentityLink(w http.ResponseWriter, r *http.Request, flow oauthFlow) error {
	if !setFlowCookie(w, oauthFlowCookieName, flow, s.cookiesSecure()) {
		return &apierr.Error{Code: apierr.Internal, Message: "could not continue linking"}
	}
	next := "/auth/" + flow.Provider + "/link/complete"
	renderFlowPage(w, http.StatusOK, flowPage{
		Title:       "Linking " + providerLabels[flow.Provider],
		Message:     "Finishing the link.",
		Refresh:     next,
		ActionHref:  next,
		ActionLabel: "Continue",
	})
	return nil
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
	noteAuditWrite(r.Context())
	redirectToSignIns(w, r, url.Values{"linked": {name}})
}
