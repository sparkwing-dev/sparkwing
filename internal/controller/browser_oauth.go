package controller

import (
	"html/template"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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
		return routepolicy.MissingEndpointPolicy()
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
		return routepolicy.MissingEndpointPolicy()
	}
	if proof.flow.Mode == oauthFlowLink {
		return s.continueIdentityLink(w, r, proof.flow)
	}
	if proof.session == nil || proof.raw == "" {
		return routepolicy.MissingEndpointPolicy()
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
