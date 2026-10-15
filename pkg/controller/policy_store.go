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
	console := lookup.Class == capability.CredentialConsole
	if console {
		lookup.Class = capability.CredentialSession
	}
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
	if console {
		if len(snapshot.Authorities) != 1 || !reader.server.consoleAccount(snapshot.Authorities[0].Principal) {
			return capability.AuthenticationSnapshot{}, &apierr.Error{Code: apierr.Forbidden, Message: "operator console requires the operator's own signed-in account"}
		}
		p := &snapshot.Authorities[0].Principal
		p.Class, p.Capability.Subject.Kind = capability.CredentialConsole, capability.SubjectOperator
		p.Capability.Grants = []capability.Grant{capability.OperatorConsole}
	}
	return snapshot, nil
}

type policyStoreAuthenticator struct{ server *Server }

func (a policyStoreAuthenticator) Authenticate(ctx context.Context, presentation capability.Presentation, route capability.RoutePolicy) (capability.Authenticated, error) {
	if presentation.Class == capability.CredentialSession {
		for _, access := range route.Access {
			if access.Credential == capability.CredentialOperator {
				presentation.Class = capability.CredentialConsole
				break
			}
		}
	}
	return capability.NewAuthenticator(policyStoreReader{a.server}).Authenticate(ctx, presentation, route)
}

func (s *Server) consoleAccount(p capability.Principal) bool {
	c := p.Capability
	return p.Class == capability.CredentialSession && p.Authority.Kind == capability.AuthoritySession &&
		c.Subject.Kind == capability.SubjectAccount && c.Parent == nil && c.Unit == "" && c.Binding.Kind == capability.BindingNone &&
		s.operatorAccount(c.Subject.ID)
}

type policyStoreBindings struct{ server *Server }

func (b policyStoreBindings) CheckBinding(ctx context.Context, p capability.Principal, rule capability.BindingRule, target capability.Target) error {
	if p.Class == capability.CredentialConsole && rule == capability.BindNone {
		return nil
	}
	return b.server.store.CheckBinding(ctx, p, rule, target)
}

func (s *Server) storeRouteAuthorization() RouteAuthorization {
	if s.store == nil {
		return RouteAuthorization{}
	}
	return RouteAuthorization{
		Authenticator: policyStoreAuthenticator{s}, Bindings: policyStoreBindings{s},
		CheckCSRF: s.checkSessionCSRF,
		Identify: func(r *http.Request) (capability.Presentation, error) {
			if len(r.CookiesNamed(cookieName(sessionCookieName, s.cookiesSecure()))) == 0 && s.peerIdentity != nil {
				return s.identifyPeer(r)
			}
			raw, err := s.sessionPresentation(r)
			return capability.Presentation{Raw: raw, Class: capability.CredentialSession}, err
		},
	}
}

func (s *Server) checkSessionCSRF(r *http.Request, p capability.Principal) error {
	raw, err := s.sessionPresentation(r)
	digest := sha256.Sum256([]byte(raw))
	if err != nil || (p.Class != capability.CredentialSession && p.Class != capability.CredentialConsole) || p.Authority != (capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}) {
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
