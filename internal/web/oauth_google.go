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
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

// safety: the flow cookie is what proves the browser finishing a sign-in is the
// one that started it; without it a callback URL someone else arranged would sign
// this browser in as them. Lax because Google returns with a cross-site top-level GET.
const oauthFlowCookieName = hostPrefix + "sw_oauth"

const oauthFlowTTL = 10 * time.Minute

const googleCallbackPath = "/auth/google/callback"

type oauthFlow struct {
	State    string `json:"state"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
}

type oauthStartResp struct {
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
	Verifier     string `json:"verifier"`
}

type oauthExchangeResp struct {
	SessionID string `json:"session_id"`
}

func googleStartHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		controllerURL := authControllerURL(opts)
		if controllerURL == "" {
			http.Error(w, "sign-in needs a controller session backend", http.StatusNotFound)
			return
		}
		next := safeNext(r.URL.Query().Get("next"))
		start, err := controllerGoogleStart(r.Context(), controllerURL, oauthRedirectURI(r))
		if err != nil {
			renderLoginPage(w, r, loginPageData{
				Next: next, Google: true, Error: "Google sign-in is unavailable right now.",
			}, http.StatusBadGateway, cookiesSecure(opts))
			return
		}
		value, err := json.Marshal(oauthFlow{State: start.State, Verifier: start.Verifier, Next: next})
		if err != nil {
			http.Error(w, "could not start sign-in", http.StatusInternalServerError)
			return
		}
		setOAuthFlowCookie(w, base64.RawURLEncoding.EncodeToString(value), int(oauthFlowTTL/time.Second), cookiesSecure(opts))
		http.Redirect(w, r, start.AuthorizeURL, http.StatusSeeOther)
	}
}

func googleCallbackHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		controllerURL := authControllerURL(opts)
		if controllerURL == "" {
			http.Error(w, "sign-in needs a controller session backend", http.StatusNotFound)
			return
		}
		secure := cookiesSecure(opts)
		query := r.URL.Query()
		refuse := func(status int, message string) {
			renderLoginPage(w, r, loginPageData{Next: "/", Google: true, Error: message}, status, secure)
		}
		if query.Get("error") != "" {
			refuse(http.StatusUnauthorized, "Google sign-in was not completed.")
			return
		}
		flow, ok := readOAuthFlow(r, secure)
		if !ok {
			refuse(http.StatusBadRequest, "This sign-in was not started in this browser. Start again.")
			return
		}
		// safety: the cookie survives a mismatch, so one forged callback cannot discard a sign-in this browser has in flight.
		if !constantTimeEqual(query.Get("state"), flow.State) {
			refuse(http.StatusBadRequest, "This sign-in could not be verified. Start again.")
			return
		}
		setOAuthFlowCookie(w, "", -1, secure)
		code := query.Get("code")
		if code == "" {
			refuse(http.StatusBadRequest, "Google sign-in was not completed.")
			return
		}
		exchanged, err := controllerGoogleExchange(r.Context(), controllerURL, code, flow.Verifier,
			oauthRedirectURI(r), ratelimit.ClientIP(r, opts.TrustedProxyCIDRs))
		if err != nil {
			refuse(http.StatusBadGateway, "Google sign-in could not be completed.")
			return
		}
		sess, err := controllerResolveSession(r.Context(), controllerURL, exchanged.SessionID)
		if err != nil {
			refuse(http.StatusBadGateway, "Google sign-in could not be completed.")
			return
		}
		setSessionCookies(w, &loginResp{SessionID: exchanged.SessionID, CSRFToken: sess.CSRFToken}, secure)
		renderSignedIn(w, safeNext(flow.Next))
	}
}

// safety: the scheme follows the TLS evidence the CSRF origin check trusts, and
// the host is the one the browser used, so Google returns to the dashboard the
// sign-in started on; Google refuses any redirect URI not registered for the client.
func oauthRedirectURI(r *http.Request) string {
	scheme := "http"
	if requestOverTLSFrom(r.Context()) {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: r.Host, Path: googleCallbackPath}).String()
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
	if err := json.Unmarshal(raw, &flow); err != nil || flow.State == "" || flow.Verifier == "" {
		return oauthFlow{}, false
	}
	return flow, true
}

func controllerGoogleStart(ctx context.Context, controllerURL, redirectURI string) (*oauthStartResp, error) {
	var out oauthStartResp
	if err := postControllerJSON(ctx, controllerURL, "/api/v1/auth/oauth/google/start", "",
		map[string]string{"redirect_uri": redirectURI}, &out); err != nil {
		return nil, err
	}
	u, err := url.Parse(out.AuthorizeURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("controller returned an unusable authorize_url")
	}
	if out.State == "" || out.Verifier == "" {
		return nil, errors.New("controller start response is missing state or verifier")
	}
	return &out, nil
}

func controllerGoogleExchange(ctx context.Context, controllerURL, code, verifier, redirectURI, clientIP string) (*oauthExchangeResp, error) {
	var out oauthExchangeResp
	if err := postControllerJSON(ctx, controllerURL, "/api/v1/auth/oauth/google/exchange", clientIP,
		map[string]string{"code": code, "verifier": verifier, "redirect_uri": redirectURI}, &out); err != nil {
		return nil, err
	}
	if out.SessionID == "" {
		return nil, errors.New("controller exchange response carries no session")
	}
	return &out, nil
}

func postControllerJSON(ctx context.Context, controllerURL, path, clientIP string, body, out any) error {
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
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if err != nil {
			return fmt.Errorf("controller %s: %d", path, resp.StatusCode)
		}
		return fmt.Errorf("controller %s: %d: %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// safety: the session cookie is SameSite=Strict, and a browser withholds it from
// the redirect that ends a navigation Google started. A page on this origin that
// moves on by itself makes the next request same-site, so the cookie rides it.
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
