package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type policyStoreReader struct{ server *Server }

func (reader policyStoreReader) ReadAuthentication(ctx context.Context, lookup capability.CredentialLookup, evidence capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	snapshot, err := reader.server.store.ReadAuthentication(ctx, lookup, evidence)
	if err != nil {
		var failure *apierr.Error
		if errors.As(err, &failure) {
			return capability.AuthenticationSnapshot{}, err
		}
		return capability.AuthenticationSnapshot{}, &apierr.Error{Code: apierr.Unavailable, Message: "authentication service unavailable"}
	}
	for _, authority := range snapshot.Authorities {
		p := authority.Principal
		if p.Class == capability.CredentialSession && (!reader.server.MultiTeam() || reader.server.sessionExpired(p.Capability.IssuedAt, snapshot.Now)) {
			return capability.AuthenticationSnapshot{}, sessionUnavailable()
		}
	}
	return snapshot, nil
}

func (s *Server) storeRouteAuthorization() RouteAuthorization {
	if s.store == nil {
		return RouteAuthorization{}
	}
	return RouteAuthorization{
		Authenticator: capability.NewAuthenticator(policyStoreReader{s}), Bindings: s.store,
		CheckCSRF: s.checkSessionCSRF,
		Identify: func(r *http.Request) (capability.Presentation, error) {
			raw, err := s.sessionPresentation(r)
			return capability.Presentation{Raw: raw, Class: capability.CredentialSession}, err
		},
	}
}

func (s *Server) checkSessionCSRF(r *http.Request, p capability.Principal) error {
	raw, err := s.sessionPresentation(r)
	digest := sha256.Sum256([]byte(raw))
	if err != nil || p.Class != capability.CredentialSession || p.Authority != (capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}) {
		return &apierr.Error{Code: apierr.Unauthenticated, Message: "session identity changed"}
	}
	session, err := s.store.LookupSession(r.Context(), raw)
	if errors.Is(err, store.ErrSessionBackend) {
		return &apierr.Error{Code: apierr.Unavailable, Message: "session service unavailable"}
	}
	if err != nil || session.Team != store.Team(p.Capability.Team) || session.AccountID != p.Capability.Subject.ID {
		return &apierr.Error{Code: apierr.Unauthenticated, Message: "session is unavailable"}
	}
	if err := s.validateSessionFlow(session); err != nil {
		return err
	}
	if !validAPIRequestCSRF(r, session.CSRFToken, s.cookiesSecure()) {
		return &apierr.Error{Code: apierr.Forbidden, Message: "invalid CSRF token"}
	}
	return nil
}
