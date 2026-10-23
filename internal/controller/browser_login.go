package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

const loginHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Sparkwing sign in</title>
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif; background: #0b0e14; color: #c9d1d9; margin: 0; display: flex; min-height: 100vh; align-items: center; justify-content: center; }
    .card { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 2rem 2.5rem; width: 100%; max-width: 360px; box-sizing: border-box; }
    h1 { font-size: 1.25rem; margin: 0 0 1.5rem 0; font-weight: 600; letter-spacing: -0.01em; }
    label { display: block; margin-bottom: 0.35rem; font-size: 0.85rem; color: #8b949e; }
    input { width: 100%; padding: 0.55rem 0.75rem; background: #0d1117; border: 1px solid #30363d; border-radius: 4px; color: #c9d1d9; font-size: 0.95rem; box-sizing: border-box; margin-bottom: 1rem; font-family: inherit; }
    input:focus { outline: none; border-color: #58a6ff; }
    button { width: 100%; padding: 0.6rem; background: #238636; color: white; border: none; border-radius: 4px; font-size: 0.95rem; font-weight: 500; cursor: pointer; }
    button:hover { background: #2ea043; }
    .idp { display: flex; align-items: center; justify-content: center; gap: 10px; width: 100%; height: 40px; padding: 0 12px; box-sizing: border-box; border-radius: 4px; font-family: Roboto, arial, sans-serif; font-size: 14px; font-weight: 500; line-height: 20px; letter-spacing: 0.25px; text-decoration: none; margin-bottom: 0.6rem; }
    .idp svg { width: 20px; height: 20px; flex: none; }
    .gsi { background: #131314; border: 1px solid #8e918f; color: #e3e3e3; }
    .gsi:hover { background: #1f1f20; }
    .gh { background: #24292f; border: 1px solid #57606a; color: #ffffff; }
    .gh:hover { background: #2f363d; }
    .or { display: flex; align-items: center; gap: 0.75rem; margin: 1.25rem 0; font-size: 0.75rem; color: #6e7681; }
    .or::before, .or::after { content: ""; flex: 1; border-top: 1px solid #30363d; }
    summary.or { cursor: pointer; list-style: none; }
    summary.or::-webkit-details-marker { display: none; }
  </style>
</head>
<body>
  <form class="card" method="POST" action="/login">
    <h1>Sparkwing</h1>
    {{if .Google}}
    <a class="idp gsi" href="/auth/google/start?next={{.Next}}">
      <svg viewBox="0 0 48 48" aria-hidden="true"><path fill="#EA4335" d="M24 9.5c3.54 0 6.71 1.22 9.21 3.6l6.85-6.85C35.9 2.38 30.47 0 24 0 14.62 0 6.51 5.38 2.56 13.22l7.98 6.19C12.43 13.72 17.74 9.5 24 9.5z"/><path fill="#4285F4" d="M46.98 24.55c0-1.57-.15-3.09-.38-4.55H24v9.02h12.94c-.58 2.96-2.26 5.48-4.78 7.18l7.73 6c4.51-4.18 7.09-10.36 7.09-17.65z"/><path fill="#FBBC05" d="M10.53 28.59c-.48-1.45-.76-2.99-.76-4.59s.27-3.14.76-4.59l-7.98-6.19C.92 16.46 0 20.12 0 24c0 3.88.92 7.54 2.56 10.78l7.97-6.19z"/><path fill="#34A853" d="M24 48c6.48 0 11.93-2.13 15.89-5.81l-7.73-6c-2.15 1.45-4.92 2.3-8.16 2.3-6.26 0-11.57-4.22-13.47-9.91l-7.98 6.19C6.51 42.62 14.62 48 24 48z"/><path fill="none" d="M0 0h48v48H0z"/></svg>
      <span>Sign in with Google</span>
    </a>
    {{end}}
    {{if .GitHub}}
    <a class="idp gh" href="/auth/github/start?next={{.Next}}">
      <svg viewBox="0 0 16 16" aria-hidden="true"><path fill="currentColor" d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"/></svg>
      <span>Sign in with GitHub</span>
    </a>
    {{end}}
    {{if or .Google .GitHub}}
    <details>
      <summary class="or">or use an account access token</summary>
      {{template "token" .}}
    </details>
    {{else}}
    {{template "token" .}}
    {{end}}
  </form>
</body>
</html>
{{define "token"}}
    <label for="token">Account access token</label>
    <input id="token" name="token" type="password" autocomplete="off" spellcheck="false" {{if not (or .Google .GitHub)}}autofocus {{end}}required>
    <input type="hidden" name="next" value="{{.Next}}">
    <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
    <button type="submit">Sign in</button>
{{end}}`

var loginTmpl = template.Must(template.New("login").Parse(loginHTML))

type loginPageData struct {
	Next      string
	CSRFToken string
	Google    bool
	GitHub    bool
}

func safeNext(next string) string {
	u, err := url.ParseRequestURI(next)
	if err != nil || strings.Contains(next, "#") || u.IsAbs() || u.Host != "" || u.Fragment != "" || u.Opaque != "" {
		return "/"
	}
	if !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") || strings.Contains(u.Path, `\`) {
		return "/"
	}
	return next
}

func (s *Server) withSignInProviders(data loginPageData) loginPageData {
	providers := s.signInProviders()
	data.Google = slices.Contains(providers, store.ProviderGoogle)
	data.GitHub = slices.Contains(providers, store.ProviderGitHub)
	return data
}

// safety: the form carries the CSRF cookie's token, minting one when the browser holds none, so the login post
// passes the same double-submit check every other form does.
func (s *Server) renderLoginPage(w http.ResponseWriter, r *http.Request, data loginPageData, status int) error {
	secure := s.cookiesSecure()
	token := ""
	if cookie, err := r.Cookie(cookieName(csrfCookieName, secure)); err == nil {
		token = cookie.Value
	}
	if token == "" {
		var err error
		if token, err = newCSRFToken(); err != nil {
			return errors.Join(&apierr.Error{Code: apierr.Internal, Message: "could not create the sign-in form"}, err)
		}
	}
	data.CSRFToken = token
	setCSRFCookie(w, token, secure, int(12*time.Hour/time.Second))
	renderPage(w, status, loginTmpl, data)
	return nil
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	proof, ok := r.Context().Value(loginProofKey{}).(loginProof)
	if !ok {
		return routepolicy.MissingEndpointPolicy()
	}
	data := loginPageData{Next: safeNext(r.URL.Query().Get("next"))}
	if proof.session != nil {
		http.Redirect(w, r, data.Next, http.StatusSeeOther)
		return nil
	}
	if len(r.CookiesNamed(cookieName(sessionCookieName, s.cookiesSecure()))) != 0 {
		clearSessionCookies(w, s.cookiesSecure())
	}
	return s.renderLoginPage(w, r, s.withSignInProviders(data), http.StatusOK)
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	proof, ok := r.Context().Value(loginProofKey{}).(loginProof)
	if !ok || proof.raw == "" || proof.session == nil {
		return routepolicy.MissingEndpointPolicy()
	}
	s.endPriorSession(r, proof.raw)
	noteAuditWrite(r.Context())
	setSessionCookies(w, proof.raw, proof.session.CSRFToken, s.cookiesSecure())
	http.Redirect(w, r, safeNext(r.PostForm.Get("next")), http.StatusSeeOther)
	return nil
}

func (s *Server) endPriorSession(r *http.Request, newSessionID string) {
	prior := sessionCookie(r, s.cookiesSecure())
	if prior == "" || prior == newSessionID {
		return
	}
	snapshot, err := s.operator.ReadAuthentication(r.Context(), capability.CredentialLookup{Class: capability.CredentialSession, Audience: capability.Controller, Digest: sha256.Sum256([]byte(newSessionID))}, capability.RequestEvidence{})
	if err != nil {
		s.logger.Warn("sign-in could not read the new session's ancestry", "err", err)
		return
	}
	digest := sha256.Sum256([]byte(prior))
	for _, state := range snapshot.Authorities {
		// Ending the minting session would also end the token used to sign in.
		if state.Principal.Authority == (capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}) {
			return
		}
	}
	if err := s.operator.DeleteSession(prior); err != nil {
		s.logger.Warn("sign-in could not end the browser's earlier session", "err", err)
	}
}

const maxFormBody = 64 << 10
