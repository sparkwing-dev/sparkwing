package routepolicy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type fixedAuthenticator struct {
	principal capability.Principal
	err       error
}

func (a fixedAuthenticator) Authenticate(_ context.Context, _ capability.Presentation, route capability.RoutePolicy) (capability.Authenticated, error) {
	return capability.Authenticated{Principal: a.principal, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, a.err
}

type fixedBindings struct{ err error }

func (b fixedBindings) CheckBinding(context.Context, capability.Principal, capability.BindingRule, capability.Target) error {
	return b.err
}

func TestRefusalHookFiresOnlyForResourceRefusals(t *testing.T) {
	member := capability.Principal{Class: capability.CredentialBearer, Capability: capability.Capability{
		Team: "b", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "acct-b"}, Audience: capability.Controller,
		Grants: []capability.Grant{capability.RunsRead}, Binding: capability.Binding{Kind: capability.BindingNone},
	}}
	ungranted := member
	ungranted.Capability.Grants = nil
	resolveOwn := func(_ context.Context, p capability.Principal, target capability.Target) (capability.Target, error) {
		target.Team = p.Capability.Team
		return target, nil
	}
	for _, tc := range []struct {
		name      string
		principal capability.Principal
		authErr   error
		resolve   func(context.Context, capability.Principal, capability.Target) (capability.Target, error)
		decodeErr error
		bindErr   error
		want      routepolicy.RefusalClass
		status    int
	}{
		{name: "missing run", principal: member, resolve: func(context.Context, capability.Principal, capability.Target) (capability.Target, error) {
			return capability.Target{}, &apierr.Error{Code: apierr.NotFound, Message: "resource not found"}
		}, want: routepolicy.RefusalNotFound, status: http.StatusNotFound},
		{name: "other team", principal: member, resolve: func(_ context.Context, _ capability.Principal, target capability.Target) (capability.Target, error) {
			target.Team = "a"
			return target, nil
		}, want: routepolicy.RefusalTeam, status: http.StatusForbidden},
		{name: "outside binding", principal: member, resolve: resolveOwn, bindErr: hosterr.Authorization(hosterr.ReasonBinding, "resource is outside the authorized binding"), want: routepolicy.RefusalBinding, status: http.StatusForbidden},
		{name: "missing grant", principal: ungranted, resolve: resolveOwn, status: http.StatusForbidden},
		{name: "unauthenticated", principal: member, authErr: &apierr.Error{Code: hosterr.Unauthenticated, Message: "credential is not live"}, resolve: resolveOwn, status: http.StatusUnauthorized},
		{name: "undecodable body", principal: member, resolve: resolveOwn, decodeErr: &apierr.Error{Code: apierr.BadRequest, Message: "bad body"}, status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var notes []routepolicy.Refusal
			router := routepolicy.New(routepolicy.Authorization{
				Authenticator: fixedAuthenticator{tc.principal, tc.authErr}, Bindings: fixedBindings{tc.bindErr},
				NoteRefusal: func(_ context.Context, r routepolicy.Refusal) { notes = append(notes, r) },
			})
			if err := router.Register(controllerwire.Endpoint{
				Method: "GET", Pattern: "/api/v1/runs/{run}", Authentication: capability.AuthCapability,
				Decode: func(r *http.Request) (capability.RequestEvidence, error) {
					return capability.RequestEvidence{Target: capability.Target{RunID: r.PathValue("run")}}, tc.decodeErr
				},
				ResolveTarget: tc.resolve,
				Handle: func(w http.ResponseWriter, _ *http.Request, _ capability.Authorized) error {
					w.WriteHeader(http.StatusNoContent)
					return nil
				},
			}); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-a", nil)
			req.Header.Set("Authorization", "Bearer swk_probe")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status = %d %s, want %d", w.Code, w.Body, tc.status)
			}
			if tc.want == "" {
				if len(notes) != 0 {
					t.Fatalf("non-resource refusal noted: %+v", notes)
				}
				return
			}
			if len(notes) != 1 || notes[0].Class != tc.want || notes[0].Pattern != "/api/v1/runs/{run}" || notes[0].Method != http.MethodGet || notes[0].Principal.Capability.Subject.ID != "acct-b" {
				t.Fatalf("notes = %+v, want one %s", notes, tc.want)
			}
		})
	}
}
