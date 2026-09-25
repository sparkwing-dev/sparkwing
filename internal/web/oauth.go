package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

// safety: the flow cookie is what proves the browser finishing a sign-in is the
// one that started it; without it a callback URL someone else arranged would sign
// this browser in as them. Lax because the provider returns with a cross-site top-level GET.
const oauthFlowCookieName = hostPrefix + "sw_oauth"

const oauthFlowTTL = 10 * time.Minute

type oauthProvider struct {
	Name  string
	Label string
}

var oauthProviders = map[string]oauthProvider{
	"google": {Name: "google", Label: "Google"},
	"github": {Name: "github", Label: "GitHub"},
}

type oauthFlow struct {
	Provider string `json:"provider"`
	State    string `json:"state"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
	// Mode is oauthFlowLink for a flow adding a sign-in to the signed-in
	// account, and empty for a sign-in.
	Mode string `json:"mode,omitempty"`
	// Code is the provider's code, kept while a link flow moves on to a
	// same-site request that carries the session.
	Code string `json:"code,omitempty"`
}

type oauthStartResp struct {
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
	Verifier     string `json:"verifier"`
}

type oauthExchangeResp struct {
	SessionID string `json:"session_id"`
}

func oauthProviderFrom(w http.ResponseWriter, r *http.Request, opts HandlerOptions) (oauthProvider, string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	provider, ok := oauthProviders[r.PathValue("provider")]
	if !ok {
		http.NotFound(w, r)
		return oauthProvider{}, "", false
	}
	controllerURL := authControllerURL(opts)
	if controllerURL == "" || !accountSessionsServed(opts) {
		http.Error(w, "account sign-in needs a dashboard running with --controller", http.StatusNotFound)
		return oauthProvider{}, "", false
	}
	return provider, controllerURL, true
}

func oauthStartHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider, controllerURL, ok := oauthProviderFrom(w, r, opts)
		if !ok {
			return
		}
		next := safeNext(r.URL.Query().Get("next"))
		start, err := controllerOAuthStart(r.Context(), controllerURL, provider.Name, oauthRedirectURI(r, provider.Name),
			ratelimit.ClientIP(r, opts.TrustedProxyCIDRs))
		if err != nil {
			renderLoginPage(w, r, withSignInProviders(r.Context(), opts, loginPageData{
				Next: next, Error: provider.Label + " sign-in is unavailable right now.",
			}), http.StatusBadGateway, cookiesSecure(opts))
			return
		}
		if !setOAuthFlow(w, oauthFlow{Provider: provider.Name, State: start.State, Verifier: start.Verifier, Next: next}, cookiesSecure(opts)) {
			http.Error(w, "could not start sign-in", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, start.AuthorizeURL, http.StatusSeeOther)
	}
}

func oauthCallbackHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider, controllerURL, ok := oauthProviderFrom(w, r, opts)
		if !ok {
			return
		}
		secure := cookiesSecure(opts)
		query := r.URL.Query()
		if flow, ok := readOAuthFlow(r, secure); ok && flow.Mode == oauthFlowLink && flow.Provider == provider.Name {
			identityLinkCallback(w, r, provider, flow, secure)
			return
		}
		if query.Get("error") != "" {
			refuseOAuth(w, r, opts, secure, http.StatusUnauthorized, provider.Label+" sign-in was not completed.")
			return
		}
		flow, ok := readOAuthFlow(r, secure)
		if !ok {
			refuseOAuth(w, r, opts, secure, http.StatusBadRequest, "This sign-in was not started in this browser. Start again.")
			return
		}
		// safety: the cookie survives a mismatch, so one forged callback cannot discard a sign-in this browser has in
		// flight. The provider is part of the match, so a code from one provider never meets another's verifier.
		if !constantTimeEqual(query.Get("state"), flow.State) || flow.Provider != provider.Name {
			refuseOAuth(w, r, opts, secure, http.StatusBadRequest, "This sign-in could not be verified. Start again.")
			return
		}
		setOAuthFlowCookie(w, "", -1, secure)
		code := query.Get("code")
		if code == "" {
			refuseOAuth(w, r, opts, secure, http.StatusBadRequest, provider.Label+" sign-in was not completed.")
			return
		}
		exchanged, err := controllerOAuthExchange(r.Context(), controllerURL, provider.Name, code, flow.Verifier,
			oauthRedirectURI(r, provider.Name), ratelimit.ClientIP(r, opts.TrustedProxyCIDRs))
		if err != nil {
			refuseOAuth(w, r, opts, secure, http.StatusBadGateway, provider.Label+" sign-in could not be completed.")
			return
		}
		sess, err := controllerResolveSession(r.Context(), controllerURL, exchanged.SessionID)
		if err != nil {
			refuseOAuth(w, r, opts, secure, http.StatusBadGateway, provider.Label+" sign-in could not be completed.")
			return
		}
		// safety: the browser's earlier session would otherwise stay live on the
		// controller after its cookie is overwritten, with nothing left to end it.
		if prior, err := r.Cookie(cookieName(sessionCookieName, secure)); err == nil &&
			prior.Value != "" && prior.Value != exchanged.SessionID {
			if err := controllerLogout(r.Context(), controllerURL, prior.Value); err != nil {
				slog.Warn("oauth sign-in could not end the browser's earlier session", "err", err)
			}
		}
		setSessionCookies(w, &loginResp{SessionID: exchanged.SessionID, CSRFToken: sess.CSRFToken}, secure)
		renderSignedIn(w, safeNext(flow.Next))
	}
}

func refuseOAuth(w http.ResponseWriter, r *http.Request, opts HandlerOptions, secure bool, status int, message string) {
	renderLoginPage(w, r, withSignInProviders(r.Context(), opts,
		loginPageData{Next: "/", Error: message}), status, secure)
}

// safety: the scheme follows the TLS evidence the CSRF origin check trusts, and
// the host is the one the browser used, so the provider returns to the dashboard the
// sign-in started on; a provider refuses any redirect URI not registered for the client.
func oauthRedirectURI(r *http.Request, provider string) string {
	return dashboardURL(r, "/auth/"+provider+"/callback")
}

func dashboardURL(r *http.Request, path string) string {
	scheme := "http"
	if requestOverTLSFrom(r.Context()) {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: r.Host, Path: path}).String()
}

func setOAuthFlow(w http.ResponseWriter, flow oauthFlow, secure bool) bool {
	value, err := json.Marshal(flow)
	if err != nil {
		return false
	}
	setOAuthFlowCookie(w, base64.RawURLEncoding.EncodeToString(value), int(oauthFlowTTL/time.Second), secure)
	return true
}

func setOAuthFlowCookie(w http.ResponseWriter, value string, maxAge int, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName(oauthFlowCookieName, secure),
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func readOAuthFlow(r *http.Request, secure bool) (oauthFlow, bool) {
	c, err := r.Cookie(cookieName(oauthFlowCookieName, secure))
	if err != nil || c.Value == "" {
		return oauthFlow{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return oauthFlow{}, false
	}
	var flow oauthFlow
	if err := json.Unmarshal(raw, &flow); err != nil || flow.Provider == "" || flow.State == "" || flow.Verifier == "" {
		return oauthFlow{}, false
	}
	return flow, true
}

// safety: the client IP keys the controller's start budget, so without it every
// browser behind this dashboard shares one bucket.
func controllerOAuthStart(ctx context.Context, controllerURL, provider, redirectURI, clientIP string) (*oauthStartResp, error) {
	var out oauthStartResp
	if err := postControllerJSON(ctx, controllerURL, "/api/v1/auth/oauth/"+provider+"/start", clientIP,
		map[string]string{"redirect_uri": redirectURI}, &out); err != nil {
		return nil, err
	}
	if !absoluteHTTPURL(out.AuthorizeURL) {
		return nil, errors.New("controller returned an unusable authorize_url")
	}
	if out.State == "" || out.Verifier == "" {
		return nil, errors.New("controller start response is missing state or verifier")
	}
	return &out, nil
}

func controllerOAuthExchange(ctx context.Context, controllerURL, provider, code, verifier, redirectURI, clientIP string) (*oauthExchangeResp, error) {
	var out oauthExchangeResp
	if err := postControllerJSON(ctx, controllerURL, "/api/v1/auth/oauth/"+provider+"/exchange", clientIP,
		map[string]string{"code": code, "verifier": verifier, "redirect_uri": redirectURI}, &out); err != nil {
		return nil, err
	}
	if out.SessionID == "" {
		return nil, errors.New("controller exchange response carries no session")
	}
	return &out, nil
}

func absoluteHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

type controllerStatusError struct {
	Path   string
	Status int
	// Code is the controller's machine-readable reason, when its answer
	// carried one beside the message.
	Code    string
	Message string
}

func (e *controllerStatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("controller %s: %d", e.Path, e.Status)
	}
	return fmt.Sprintf("controller %s: %d: %s", e.Path, e.Status, e.Message)
}

func postControllerJSON(ctx context.Context, controllerURL, path, clientIP string, body, out any) error {
	return postControllerJSONAs(ctx, controllerURL, path, clientIP, "", body, out)
}

func postControllerJSONAs(ctx context.Context, controllerURL, path, clientIP, sessionID string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(controllerURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if clientIP != "" {
		req.Header.Set("X-Forwarded-For", clientIP)
	}
	if sessionID != "" {
		req.Header.Set("Authorization", sessionAuthorization(sessionID))
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if err != nil {
			return &controllerStatusError{Path: path, Status: resp.StatusCode}
		}
		return &controllerStatusError{
			Path: path, Status: resp.StatusCode, Code: controllerErrorCode(msg), Message: controllerErrorMessage(msg),
		}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// controllerErrorCode reads the code of an answer shaped {"error": code,
// "message": text}. A bare {"error": text} carries a message, not a code.
func controllerErrorCode(body []byte) string {
	var parsed struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Message == "" {
		return ""
	}
	return parsed.Error
}

func controllerErrorMessage(body []byte) string {
	var parsed struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if parsed.Message != "" {
			return parsed.Message
		}
		if parsed.Error != "" {
			return parsed.Error
		}
	}
	return strings.TrimSpace(string(body))
}

// safety: the provider returns through this origin before the browser follows
// the vetted next path, so the session never rides an untrusted redirect target.
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

func renderSignedIn(w http.ResponseWriter, next string) {
	var page bytes.Buffer
	if err := signedInTmpl.Execute(&page, next); err != nil {
		http.Error(w, "signed in; continue to "+next, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// safety: the session cookies are already set; a client that went away mid-page signs in on its next request.
	if _, err := page.WriteTo(w); err != nil {
		return
	}
}
