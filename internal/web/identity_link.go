package web

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

const oauthFlowLink = "link"

const signInsSettingsPath = "/account/sign-ins"

// safety: the settings page shows fixed text for each of these codes and
// nothing the URL carries, so a crafted link cannot put words on the page.
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

func identityLinkRefusal(err error) string {
	var refused *controllerStatusError
	if errors.As(err, &refused) && identityLinkRefusals[refused.Code] {
		return refused.Code
	}
	return identityLinkFailed
}

func redirectToSignIns(w http.ResponseWriter, r *http.Request, query url.Values) {
	http.Redirect(w, r, (&url.URL{Path: signInsSettingsPath, RawQuery: query.Encode()}).String(), http.StatusSeeOther)
}

func refuseIdentityLink(w http.ResponseWriter, r *http.Request, provider, code string) {
	redirectToSignIns(w, r, url.Values{"refused": {code}, "provider": {provider}})
}

func linkPrincipal(w http.ResponseWriter, r *http.Request) (*webPrincipal, bool) {
	principal, ok := WebPrincipalFromContext(r.Context())
	if !ok || principal.sessionID == "" {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(signInsSettingsPath), http.StatusSeeOther)
		return nil, false
	}
	return principal, true
}

// identityLinkHandler starts adding a provider's sign-in to the signed-in
// account. The controller signs a state bound to the account and this
// session; the browser keeps it, with the verifier, in the same flow cookie
// sign-in uses, marked as a link.
func identityLinkHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider, controllerURL, ok := oauthProviderFrom(w, r, opts)
		if !ok {
			return
		}
		secure := cookiesSecure(opts)
		principal, ok := linkPrincipal(w, r)
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
		var start oauthStartResp
		err := postControllerJSONAs(r.Context(), controllerURL, "/api/v1/me/identities/"+provider.Name+"/link",
			ratelimit.ClientIP(r, opts.TrustedProxyCIDRs), principal.sessionID,
			map[string]string{"redirect_uri": oauthRedirectURI(r, provider.Name)}, &start)
		if err != nil {
			refuseIdentityLink(w, r, provider.Name, identityLinkRefusal(err))
			return
		}
		if !absoluteHTTPURL(start.AuthorizeURL) || start.State == "" || start.Verifier == "" {
			refuseIdentityLink(w, r, provider.Name, identityLinkFailed)
			return
		}
		if !setOAuthFlow(w, oauthFlow{Provider: provider.Name, State: start.State, Verifier: start.Verifier, Mode: oauthFlowLink}, secure) {
			http.Error(w, "could not start linking", http.StatusInternalServerError)
			return
		}
		// safety: the page's form-action 'self' stops a browser following a form's redirect to
		// the provider, and a page on this origin that moves on by itself is a navigation.
		renderFlowPage(w, http.StatusOK, flowPage{
			Title:       "Linking " + provider.Label,
			Message:     "Continuing to " + provider.Label + " to confirm the account.",
			Refresh:     start.AuthorizeURL,
			ActionHref:  start.AuthorizeURL,
			ActionLabel: "Continue to " + provider.Label,
		})
	}
}

// safety: the session cookie is SameSite=Strict and a browser withholds it from the provider's
// return, so the callback keeps the code in the flow cookie and moves on from a page on this
// origin, whose next request carries the session the controller checks the state against.
func identityLinkCallback(w http.ResponseWriter, r *http.Request, provider oauthProvider, flow oauthFlow, secure bool) {
	query := r.URL.Query()
	if query.Get("error") != "" {
		setOAuthFlowCookie(w, "", -1, secure)
		refuseIdentityLink(w, r, provider.Name, "provider_rejected")
		return
	}
	// safety: the cookie survives a mismatch, so one forged return cannot discard a link in flight.
	if !constantTimeEqual(query.Get("state"), flow.State) || query.Get("code") == "" {
		renderFlowPage(w, http.StatusBadRequest, flowPage{
			Title:       provider.Label + " was not linked",
			Message:     "This link could not be verified. Start again.",
			ActionHref:  signInsSettingsPath,
			ActionLabel: "Back to linked sign-ins",
		})
		return
	}
	flow.Code = query.Get("code")
	if !setOAuthFlow(w, flow, secure) {
		http.Error(w, "could not continue linking", http.StatusInternalServerError)
		return
	}
	next := "/auth/" + provider.Name + "/link/complete"
	renderFlowPage(w, http.StatusOK, flowPage{
		Title:       "Linking " + provider.Label,
		Message:     "Finishing the link.",
		Refresh:     next,
		ActionHref:  next,
		ActionLabel: "Continue",
	})
}

func identityLinkCompleteHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider, controllerURL, ok := oauthProviderFrom(w, r, opts)
		if !ok {
			return
		}
		secure := cookiesSecure(opts)
		principal, ok := linkPrincipal(w, r)
		if !ok {
			return
		}
		flow, ok := readOAuthFlow(r, secure)
		if !ok || flow.Mode != oauthFlowLink || flow.Provider != provider.Name || flow.Code == "" {
			refuseIdentityLink(w, r, provider.Name, "link_state_invalid")
			return
		}
		setOAuthFlowCookie(w, "", -1, secure)
		var linked struct {
			Provider string `json:"provider"`
		}
		err := postControllerJSONAs(r.Context(), controllerURL, "/api/v1/me/identities/"+provider.Name+"/link/complete",
			ratelimit.ClientIP(r, opts.TrustedProxyCIDRs), principal.sessionID, map[string]string{
				"state":        flow.State,
				"verifier":     flow.Verifier,
				"code":         flow.Code,
				"redirect_uri": oauthRedirectURI(r, provider.Name),
			}, &linked)
		if err != nil {
			refuseIdentityLink(w, r, provider.Name, identityLinkRefusal(err))
			return
		}
		redirectToSignIns(w, r, url.Values{"linked": {provider.Name}})
	}
}
