package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) storeRouteAuthorization() RouteAuthorization {
	if s.store == nil {
		return RouteAuthorization{}
	}
	return RouteAuthorization{
		Authenticator: capability.NewAuthenticator(s.store), Bindings: s.store,
		CheckCSRF: s.checkSessionCSRF,
		Identify: func(r *http.Request) (capability.Presentation, error) {
			return capability.Presentation{Raw: sessionCookie(r, s.cookiesSecure()), Class: capability.CredentialSession}, nil
		},
	}
}

func (s *Server) checkSessionCSRF(r *http.Request, p capability.Principal) error {
	raw := sessionCookie(r, s.cookiesSecure())
	digest := sha256.Sum256([]byte(raw))
	if raw == "" || p.Class != capability.CredentialSession || p.Authority != (capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}) {
		return &apierr.Error{Code: apierr.Unauthenticated, Message: "session identity changed"}
	}
	session, err := s.store.LookupSession(r.Context(), raw)
	if errors.Is(err, store.ErrSessionBackend) {
		return &apierr.Error{Code: apierr.Unavailable, Message: "session service unavailable"}
	}
	if err != nil || session.Team != store.Team(p.Capability.Team) || session.AccountID != p.Capability.Subject.ID {
		return &apierr.Error{Code: apierr.Unauthenticated, Message: "session is unavailable"}
	}
	if !validAPIRequestCSRF(r, session.CSRFToken, s.cookiesSecure()) {
		return &apierr.Error{Code: apierr.Forbidden, Message: "invalid CSRF token"}
	}
	return nil
}
