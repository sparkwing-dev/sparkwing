package controller

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type flowRefusal struct {
	status  int
	code    string
	message string
}

func (r *flowRefusal) Error() string { return r.message }

func refuse(status int, code, message string) *flowRefusal {
	return &flowRefusal{status: status, code: code, message: message}
}

func refusalOf(err error) (*flowRefusal, bool) {
	var refused *flowRefusal
	ok := errors.As(err, &refused)
	return refused, ok
}

func identityRefusal(op string, err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrNotMember):
		return refuse(http.StatusNotFound, "", err.Error())
	case errors.Is(err, store.ErrInvalidSlug), errors.Is(err, store.ErrInvalidInput):
		return refuse(http.StatusBadRequest, "", err.Error())
	case errors.Is(err, store.ErrSlugTaken), errors.Is(err, store.ErrAlreadyMember),
		errors.Is(err, store.ErrInvitationOpen):
		return refuse(http.StatusConflict, "", err.Error())
	case errors.Is(err, store.ErrLastOwner), errors.Is(err, store.ErrRoleAboveOwn),
		errors.Is(err, store.ErrEmailMismatch), errors.Is(err, store.ErrTeamLimit), errors.Is(err, store.ErrTeamFull),
		errors.Is(err, store.ErrWaitlisted), errors.Is(err, store.ErrBindingLimit):
		return refuse(http.StatusForbidden, "", err.Error())
	}
	return fmt.Errorf("%s: %w", op, err)
}

type oauthGrant struct {
	AuthorizeURL string
	State        string
	Verifier     string
}

type oauthFlowProof struct {
	State    string
	Verifier string
	Code     string
}

const flowCookieTTL = 10 * time.Minute

// safety: the flow cookie is what proves the browser finishing a flow is the one that started it, and it holds the
// verifier only that browser has. Lax because the provider returns with a cross-site top-level GET.
func setFlowCookie(w http.ResponseWriter, name string, v any, secure bool) bool {
	raw, err := json.Marshal(v)
	if err != nil {
		return false
	}
	writeFlowCookie(w, name, base64.RawURLEncoding.EncodeToString(raw), int(flowCookieTTL/time.Second), secure)
	return true
}

func clearFlowCookie(w http.ResponseWriter, name string, secure bool) {
	writeFlowCookie(w, name, "", -1, secure)
}

func writeFlowCookie(w http.ResponseWriter, name, value string, maxAge int, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName(name, secure),
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func readFlowCookie(r *http.Request, name string, secure bool, v any) bool {
	c, err := r.Cookie(cookieName(name, secure))
	if err != nil || c.Value == "" {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, v) == nil
}

// safety: the scheme follows the TLS evidence the CSRF origin check trusts, and the host is the one the browser
// used, so the provider returns to the origin the flow started on; a provider refuses any redirect URI not
// registered for the client, and this controller refuses one not on its allowlist.
func browserURL(r *http.Request, path string) string {
	scheme := "http"
	if web.RequestOverTLS(r.Context()) {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: r.Host, Path: path}).String()
}

func absoluteHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

type flowPage struct {
	Title       string
	Message     string
	Refresh     string
	ActionHref  string
	ActionLabel string
}

var flowPageTmpl = template.Must(template.New("flow").Parse(`<!doctype html>
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

func renderFlowPage(w http.ResponseWriter, status int, page flowPage) {
	renderPage(w, status, flowPageTmpl, page)
}

func renderPage(w http.ResponseWriter, status int, tmpl *template.Template, data any) {
	var body bytes.Buffer
	if err := tmpl.Execute(&body, data); err != nil {
		http.Error(w, "could not render this page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// safety: a client that went away mid-page starts again from its next request.
	if _, err := body.WriteTo(w); err != nil {
		return
	}
}
