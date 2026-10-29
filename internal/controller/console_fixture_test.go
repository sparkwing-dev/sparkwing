package controller

import (
	"context"
	"net/http"

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
	if presentation.Class != capability.CredentialSession {
		return capability.Authenticated{}, &apierr.Error{Code: apierr.Unauthenticated}
	}
	session, err := f.server.operator.LookupSession(ctx, presentation.Raw)
	if err != nil {
		return capability.Authenticated{}, &apierr.Error{Code: apierr.Unauthenticated}
	}
	p.Class = capability.CredentialSession
	p.Capability.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: session.AccountID}
	set := f.server.operators.Load()
	if set != nil && (*set)[session.AccountID] {
		p.Class = capability.CredentialConsole
		p.Capability.Grants = []capability.Grant{capability.OperatorConsole}
	}
	var live []capability.LiveAccess
	for i := range route.Access {
		live = append(live, capability.LiveAccess{RuleIndex: i})
	}
	return capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: live}, nil
}

func (consoleFixtureAuthority) CheckBinding(_ context.Context, _ capability.Principal, rule capability.BindingRule, _ capability.Target) error {
	if rule != capability.BindNone {
		return &apierr.Error{Code: apierr.PolicyMissing}
	}
	return nil
}

// ConsoleFixtureRequest presents the existing fixture session as a browser would.
func ConsoleFixtureRequest(input interface{ fixtureServer() *Server }, r *http.Request, raw string) error {
	s := input.fixtureServer()
	r.Header.Del("Authorization")
	r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: raw})
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil
	}
	session, err := s.operator.LookupSession(r.Context(), raw)
	if err != nil {
		return err
	}
	r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: session.CSRFToken})
	r.Header.Set(csrfHeaderName, session.CSRFToken)
	return nil
}
