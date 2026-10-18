package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"time"

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
		return capability.AuthenticationSnapshot{}, errors.Join(&apierr.Error{Code: apierr.Unavailable, Message: "authentication service unavailable"}, err)
	}
	for _, authority := range snapshot.Authorities {
		p := authority.Principal
		if p.Class == capability.CredentialSession && (!reader.server.MultiTeam() || reader.server.sessionExpired(p.Capability.IssuedAt, snapshot.Now)) {
			return capability.AuthenticationSnapshot{}, sessionUnavailable()
		}
	}
	if console {
		if len(snapshot.Authorities) != 1 || !reader.server.consoleAuthority(snapshot.Authorities[0].Principal, snapshot.Now) {
			return capability.AuthenticationSnapshot{}, apierr.Authorization(apierr.ReasonMissingGrant, "operator console requires the operator's own signed-in account")
		}
		p := &snapshot.Authorities[0].Principal
		p.Class, p.Capability.Subject.Kind, p.Capability.Team = capability.CredentialConsole, capability.SubjectOperator, ""
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
	authenticated, err := capability.NewAuthenticator(policyStoreReader{a.server}).Authenticate(ctx, presentation, route)
	if err != nil {
		return capability.Authenticated{}, err
	}
	if p := authenticated.Principal; p.Class == capability.CredentialConsole && unsafeMethod(route.Method) && !recentSignIn(p.Capability.IssuedAt, time.Now()) {
		return capability.Authenticated{}, apierr.Authorization(apierr.ReasonReauthRequired, "operator console changes need a sign-in from the last 10 minutes; sign out, sign in again and retry")
	}
	if capability.DeploymentAuthority(authenticated.Principal) && !a.server.deploymentBearerEligible() && slices.ContainsFunc(route.Access, func(rule capability.AccessRule) bool { return rule.Credential == capability.CredentialDeployment }) {
		return capability.Authenticated{}, sessionUnavailable()
	}
	a.server.masker.Register(presentation.Raw)
	return authenticated, nil
}

func recentSignIn(at, now time.Time) bool {
	return !at.After(now) && now.Sub(at) <= store.IdentityChangeSignInWindow
}

func (s *Server) consoleAccount(p capability.Principal) bool {
	return s.consoleAuthority(p, time.Now())
}

func (s *Server) consoleAuthority(p capability.Principal, now time.Time) bool {
	return capability.ConsoleAuthority(p, s.MultiTeam() && s.operatorAccount(p.Capability.Subject.ID), now, s.sessionMaxLifetime)
}

func (s *Server) deploymentBearerEligible() bool {
	return s.identity.license == nil || s.cloudDeployment
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
		return errors.Join(&apierr.Error{Code: apierr.Unavailable, Message: "session service unavailable"}, err)
	}
	if err != nil || p.Class != capability.CredentialConsole && session.Team != store.Team(p.Capability.Team) || session.AccountID != p.Capability.Subject.ID {
		return &apierr.Error{Code: apierr.Unauthenticated, Message: "session is unavailable"}
	}
	if err := s.validateSessionFlow(session); err != nil {
		return err
	}
	if !validAPIRequestCSRF(r, session.CSRFToken, s.cookiesSecure()) {
		return apierr.Authorization(apierr.ReasonBinding, "invalid CSRF token")
	}
	return nil
}

// WithCloudDeployment selects hosted deployment credential eligibility.
func (s *Server) WithCloudDeployment(cloud bool) *Server { s.cloudDeployment = cloud; return s }
