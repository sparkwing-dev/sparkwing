package controller

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func (s *Server) browserSessionEndpoints() []controllerwire.Endpoint {
	return []controllerwire.Endpoint{
		{Method: "POST", Pattern: "/logout", Authentication: capability.AuthBrowserFlow, Verify: s.verifyBrowserLogout, Handle: s.handleBrowserLogout},
	}
}

func (s *Server) verifyBrowserLogout(r *http.Request) error {
	session, err := s.readSessionFlow(r)
	if err != nil {
		return err
	}
	r.Body = http.MaxBytesReader(nil, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		return invalidEngineBody("invalid sign-out form")
	}
	if !validFormCSRF(r, s.cookiesSecure()) || !constantTimeEqual(r.PostForm.Get("csrf_token"), session.CSRFToken) {
		return apierr.Authorization(apierr.ReasonReauthRequired, "invalid CSRF token")
	}
	notePolicyPrincipal(r.Context(), capability.Principal{Class: capability.CredentialSession, Capability: capability.Capability{
		Team: string(session.Team), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: session.AccountID},
	}})
	*r = *r.WithContext(context.WithValue(r.Context(), sessionProofKey{}, sessionProof{session: session}))
	return nil
}

func (s *Server) handleBrowserLogout(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	if err := s.endVerifiedSession(w, r); err != nil {
		return err
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
	return nil
}
