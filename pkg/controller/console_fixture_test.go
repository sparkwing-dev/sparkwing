package controller

import (
	"context"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// safety: handler fixtures use existing identity rows while the committed-fact
// reader is implemented separately. Acceptance tests exercise the real verifier.
type consoleFixtureAuthority struct{ server *Server }

func (f consoleFixtureAuthority) Authenticate(ctx context.Context, presentation capability.Presentation, route capability.RoutePolicy) (capability.Authenticated, error) {
	p := capability.Principal{Class: presentation.Class, Capability: capability.Capability{
		Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNone},
	}}
	if presentation.Class == capability.CredentialBearer {
		if _, err := f.server.store.LookupToken(presentation.Raw, time.Now()); err != nil {
			return capability.Authenticated{}, &apierr.Error{Code: apierr.Unauthenticated}
		}
		p.Capability.Subject = capability.Subject{Kind: capability.SubjectOperator, ID: "deployment"}
	} else {
		session, err := f.server.store.LookupSession(ctx, presentation.Raw, time.Now())
		if err != nil {
			return capability.Authenticated{}, &apierr.Error{Code: apierr.Unauthenticated}
		}
		p.Class = capability.CredentialSession
		p.Capability.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: session.AccountID}
		set := f.server.operators.Load()
		if set != nil && (*set)[session.AccountID] {
			p.Class, p.Capability.Subject.Kind = capability.CredentialConsole, capability.SubjectOperator
			p.Capability.Grants = []capability.Grant{capability.OperatorConsole}
		}
	}
	return capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, nil
}

func (consoleFixtureAuthority) CheckBinding(_ context.Context, _ capability.Principal, rule capability.BindingRule, _ capability.Target) error {
	if rule != capability.BindNone {
		return &apierr.Error{Code: apierr.PolicyMissing}
	}
	return nil
}

// ConfigureConsoleFixture isolates handler tests from the unfinished store reader.
func ConfigureConsoleFixture(s *Server) {
	f := consoleFixtureAuthority{s}
	s.WithAuthorization(RouteAuthorization{
		Authenticator: f, Bindings: f,
		Identify: func(r *http.Request) (capability.Presentation, error) {
			raw := sessionCookie(r, s.cookiesSecure())
			if raw == "" {
				return capability.Presentation{}, &apierr.Error{Code: apierr.Unauthenticated}
			}
			return capability.Presentation{Raw: raw, Class: capability.CredentialSession}, nil
		},
		CheckCSRF: func(r *http.Request, _ capability.Principal) error {
			session, err := s.store.LookupSession(r.Context(), sessionCookie(r, s.cookiesSecure()), time.Now())
			if err != nil || !validAPIRequestCSRF(r, session.CSRFToken, s.cookiesSecure()) {
				return &apierr.Error{Code: apierr.Forbidden, Message: "invalid CSRF token"}
			}
			return nil
		},
	})
}

// ConsoleFixtureRequest presents the existing fixture session as a browser would.
func ConsoleFixtureRequest(s *Server, r *http.Request, raw string) error {
	session, err := s.store.LookupSession(r.Context(), raw, time.Now())
	if err != nil {
		return err
	}
	r.Header.Del("Authorization")
	r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: raw})
	r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: session.CSRFToken})
	r.Header.Set(csrfHeaderName, session.CSRFToken)
	return nil
}
