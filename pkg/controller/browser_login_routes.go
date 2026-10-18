package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type loginProofKey struct{}

type loginProof struct {
	raw     string
	session *store.Session
}

func (s *Server) loginEndpoints() []Endpoint {
	return []Endpoint{
		{Method: "GET", Pattern: "/login", Authentication: capability.AuthBrowserFlow, Verify: s.verifyLoginPage, Handle: s.handleLoginPage},
		{Method: "POST", Pattern: "/login", Authentication: capability.AuthBrowserFlow, Verify: s.verifyLoginSubmit, Handle: s.handleLoginSubmit},
	}
}

func (s *Server) verifyLoginPage(r *http.Request) error {
	proof := loginProof{}
	if len(r.Header.Values("Authorization")) != 0 {
		return sessionUnavailable()
	}
	if len(r.CookiesNamed(cookieName(sessionCookieName, s.cookiesSecure()))) != 0 {
		var err error
		proof.session, err = s.readSessionFlow(r)
		var failure *apierr.Error
		if errors.As(err, &failure) && failure.Code != apierr.Unauthenticated {
			return err
		}
		if err != nil {
			proof.session = nil
		}
	}
	*r = *r.WithContext(context.WithValue(r.Context(), loginProofKey{}, proof))
	return nil
}

func (s *Server) verifyLoginSubmit(r *http.Request) error {
	if err := s.loginLimit.admit(r); err != nil {
		return err
	}
	if err := s.readLoginForm(r); err != nil {
		return err
	}
	if s.store == nil || s.authorization.Authenticator == nil {
		return missingEndpointPolicy()
	}
	if !s.MultiTeam() {
		return sessionUnavailable()
	}
	route, _ := capability.LookupRoute("GET", "/api/v1/auth/whoami")
	auth, err := s.authorization.Authenticator.Authenticate(r.Context(), capability.Presentation{Class: capability.CredentialBearer, Raw: strings.TrimSpace(r.PostForm.Get("token"))}, route)
	if err != nil {
		s.loginLimit.failed()
		return err
	}
	p, c := auth.Principal, auth.Principal.Capability
	if p.Class != capability.CredentialBearer || c.Subject.Kind != capability.SubjectAccount || c.Audience != capability.Controller || c.Team == "" || c.Unit != "" || c.Purpose != "" || c.Binding.Kind != capability.BindingNone || c.Data != nil {
		s.loginLimit.failed()
		return apierr.Authorization(apierr.ReasonBinding, "sign-in requires an unbound account access token without data scope")
	}
	raw, session, err := s.store.CreateCapabilitySession(r.Context(), c.Prefix, s.sessionInitialTTL())
	if err != nil {
		var failure *apierr.Error
		if errors.As(err, &failure) {
			return err
		}
		return errors.Join(&apierr.Error{Code: apierr.Unavailable, Message: "sign-in service unavailable"}, err)
	}
	notePolicyPrincipal(r.Context(), p)
	*r = *r.WithContext(context.WithValue(r.Context(), loginProofKey{}, loginProof{raw: raw, session: session}))
	return nil
}

func (s *Server) readLoginForm(r *http.Request) error {
	if len(r.Header.Values("Authorization")) != 0 {
		return sessionUnavailable()
	}
	if len(r.CookiesNamed(cookieName(sessionCookieName, s.cookiesSecure()))) > 1 {
		return sessionUnavailable()
	}
	if !sameOriginRequest(r) {
		return apierr.Authorization(apierr.ReasonReauthRequired, "invalid CSRF token")
	}
	r.Body = http.MaxBytesReader(nil, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		return invalidEngineBody("invalid sign-in form")
	}
	if len(r.CookiesNamed(cookieName(csrfCookieName, s.cookiesSecure()))) != 1 || len(r.PostForm["csrf_token"]) != 1 || !validFormCSRF(r, s.cookiesSecure()) {
		return apierr.Authorization(apierr.ReasonReauthRequired, "invalid CSRF token")
	}
	for key, values := range r.PostForm {
		if key != "token" && key != "next" && key != "csrf_token" || len(values) != 1 {
			return invalidEngineBody("invalid sign-in form")
		}
	}
	if len(r.PostForm["token"]) != 1 {
		return invalidEngineBody("account access token is required")
	}
	return nil
}
