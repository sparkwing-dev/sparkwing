package capability_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

type bindingCheck func(context.Context, capability.Principal, capability.BindingRule, capability.Target) error

func (f bindingCheck) CheckBinding(ctx context.Context, p capability.Principal, b capability.BindingRule, target capability.Target) error {
	return f(ctx, p, b, target)
}

func allowBinding(context.Context, capability.Principal, capability.BindingRule, capability.Target) error {
	return nil
}

func workPrincipal() capability.Principal {
	return capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "work"}, Capability: capability.Capability{
		Prefix: "work", Team: "team", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "machine"}, Audience: capability.Controller,
		Issuance: capability.Award, Unit: capability.UnitNode, UnitID: "claim", Purpose: capability.PurposeWork, Delivery: capability.DeliveryDirect,
		Grants:  []capability.Grant{capability.NodeReport, capability.NodeResult, capability.DataRead, capability.SourceRead},
		Binding: capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Generation: 2, Holder: "holder", Attempt: 1},
	}}
}

func requireCode(t *testing.T, err error, code apierr.Code) {
	t.Helper()
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func TestAuthorizeCompleteAlternatives(t *testing.T) {
	route, ok := capability.LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/transition")
	if !ok {
		t.Fatal("transition policy absent")
	}
	auth := capability.Authenticated{Principal: workPrincipal(), Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}
	target := capability.Target{Team: "team", RunID: "run", NodeID: "job"}
	if _, err := capability.Authorize(context.Background(), bindingCheck(allowBinding), route, auth, target); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*capability.Authenticated, *capability.Target)
		code   apierr.Code
	}{
		{"audience", func(a *capability.Authenticated, _ *capability.Target) {
			a.Principal.Capability.Audience = capability.NodeHost
		}, apierr.Forbidden},
		{"team", func(_ *capability.Authenticated, x *capability.Target) { x.Team = "foreign" }, apierr.Forbidden},
		{"job", func(_ *capability.Authenticated, x *capability.Target) { x.NodeID = "sibling" }, apierr.Forbidden},
		{"run", func(_ *capability.Authenticated, x *capability.Target) { x.RunID = "foreign" }, apierr.Forbidden},
		{"grant", func(a *capability.Authenticated, _ *capability.Target) { a.Principal.Capability.Grants = nil }, apierr.Forbidden},
		{"dead", func(a *capability.Authenticated, _ *capability.Target) { a.Live = nil }, apierr.Unauthenticated},
		{"other route", func(a *capability.Authenticated, _ *capability.Target) { a.Pattern = "/other" }, apierr.PolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, x := auth, target
			tc.change(&a, &x)
			_, err := capability.Authorize(context.Background(), bindingCheck(allowBinding), route, a, x)
			requireCode(t, err, tc.code)
		})
	}
	// safety: each alternative must carry its own live evidence, even if grants overlap.
	route.Access = []capability.AccessRule{
		{Credential: capability.CredentialClaim, Grants: []capability.Grant{capability.NodeResult}, Binding: capability.BindOwnNode, Liveness: capability.LiveResult},
		{Credential: capability.CredentialClaim, Grants: []capability.Grant{capability.SourceRead}, Binding: capability.BindSource, Liveness: capability.LiveSensitive},
	}
	auth.Principal.Capability.Grants = []capability.Grant{capability.SourceRead}
	auth.Live = []capability.LiveAccess{{RuleIndex: 0, ResultReplay: true}}
	_, err := capability.Authorize(context.Background(), bindingCheck(allowBinding), route, auth, target)
	requireCode(t, err, apierr.Forbidden)
	_, err = capability.Authorize(context.Background(), nil, route, auth, target)
	requireCode(t, err, apierr.PolicyMissing)
}

func TestDelegateCannotWiden(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	end := now.Add(time.Hour)
	p := workPrincipal()
	p.Capability.ExpiresAt = &end
	p.Capability.Binding.Deadline = &end
	p.Capability.Repositories = []string{"github.com/team/repo"}
	p.Capability.Data = &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "github.com/team/repo", RunID: "run", NodeID: "job", Generation: 2, Attempt: 1, WriteRef: "manual:main", ReadRefs: []string{"manual:main", "main"}}
	child := capability.DelegateSpec{Parent: p.Authority, Audience: capability.NodeHost, Grants: []capability.Grant{capability.DataRead}, Binding: p.Capability.Binding, Repositories: p.Capability.Repositories, Data: p.Capability.Data, ExpiresAt: &end}
	if err := capability.ValidateDelegation(p, child, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*capability.DelegateSpec)
	}{
		{"grant", func(c *capability.DelegateSpec) { c.Grants = []capability.Grant{capability.OperatorConsole} }},
		{"unknown grant", func(c *capability.DelegateSpec) { c.Grants = []capability.Grant{"anything"} }},
		{"audience", func(c *capability.DelegateSpec) { c.Audience = "cache" }},
		{"parent", func(c *capability.DelegateSpec) { c.Parent.ID = "other" }},
		{"unbound", func(c *capability.DelegateSpec) { c.Binding = capability.Binding{Kind: capability.BindingNone} }},
		{"run", func(c *capability.DelegateSpec) { c.Binding.RunID = "other" }},
		{"job", func(c *capability.DelegateSpec) { c.Binding.NodeID = "other" }},
		{"generation", func(c *capability.DelegateSpec) { c.Binding.Generation++ }},
		{"holder", func(c *capability.DelegateSpec) { c.Binding.Holder = "other" }},
		{"repositories", func(c *capability.DelegateSpec) { c.Repositories = []string{"github.com/other/repo"} }},
		{"unlimited expiry", func(c *capability.DelegateSpec) { c.ExpiresAt = nil }},
		{"expiry", func(c *capability.DelegateSpec) { later := end.Add(time.Second); c.ExpiresAt = &later }},
		{"data ref", func(c *capability.DelegateSpec) { data := *c.Data; data.WriteRef = "main"; c.Data = &data }},
		{"data run", func(c *capability.DelegateSpec) { data := *c.Data; data.RunID = "other"; c.Data = &data }},
		{"data use", func(c *capability.DelegateSpec) { data := *c.Data; data.Use = capability.DataPublish; c.Data = &data }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := child
			tc.change(&c)
			requireCode(t, capability.ValidateDelegation(p, c, now), apierr.Forbidden)
		})
	}
	p.Capability.RevokedAt = &now
	requireCode(t, capability.ValidateDelegation(p, child, now), apierr.Unauthenticated)
}

func TestOwnJobInputHasNoAdminBypass(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/runs/{run}/local-host/nodes/{job}/data-authority"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			route, ok := capability.LookupRoute(tc.method, tc.path)
			if !ok {
				t.Fatal("missing policy")
			}
			p := workPrincipal()
			target := capability.Target{Team: "team", RunID: "run", NodeID: "job", SlotRunID: "run", SlotNodeID: "job", SlotGeneration: 2}
			auth := capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}
			if _, err := capability.Authorize(context.Background(), bindingCheck(allowBinding), route, auth, target); err != nil {
				t.Fatal(err)
			}
			target.NodeID, target.SlotNodeID = "sibling", "sibling"
			_, err := capability.Authorize(context.Background(), bindingCheck(allowBinding), route, auth, target)
			requireCode(t, err, apierr.Forbidden)
			auth.Principal.Capability.Grants = capability.Grants()
			auth.Principal.Capability.Subject.Kind = capability.SubjectOperator
			auth.Principal.Capability.Unit = ""
			_, err = capability.Authorize(context.Background(), bindingCheck(allowBinding), route, auth, target)
			requireCode(t, err, apierr.Forbidden)
		})
	}
}
