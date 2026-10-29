package capability

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

type bindingCheck func(context.Context, Principal, BindingRule, Target) error

func (f bindingCheck) CheckBinding(ctx context.Context, p Principal, b BindingRule, target Target) error {
	return f(ctx, p, b, target)
}

func allowBinding(context.Context, Principal, BindingRule, Target) error {
	return nil
}

func workPrincipal() Principal {
	return Principal{Class: CredentialBearer, Authority: AuthorityRef{Kind: AuthorityCapability, ID: "work"}, Capability: Capability{
		Prefix: "work", Team: "team", Subject: Subject{Kind: SubjectMachine, ID: "machine"}, Audience: Controller,
		Issuance: Award, Unit: UnitNode, UnitID: "claim", Purpose: PurposeWork, Delivery: DeliveryDirect,
		Grants:  []Grant{NodeReport, NodeResult, DataRead, SourceRead},
		Binding: Binding{Kind: BindingNode, RunID: "run", NodeID: "job", Generation: 2, Holder: "holder", Attempt: 1},
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
	route, ok := LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/transition")
	if !ok {
		t.Fatal("transition policy absent")
	}
	auth := Authenticated{Principal: workPrincipal(), Method: route.Method, Pattern: route.Path, Live: []LiveAccess{{RuleIndex: 0}}}
	target := Target{Team: "team", RunID: "run", NodeID: "job"}
	if _, err := Authorize(context.Background(), bindingCheck(allowBinding), route, auth, target); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Authenticated, *Target)
		code   apierr.Code
	}{
		{"audience", func(a *Authenticated, _ *Target) {
			a.Principal.Capability.Audience = NodeHost
		}, apierr.Forbidden},
		{"team", func(_ *Authenticated, x *Target) { x.Team = "foreign" }, apierr.Forbidden},
		{"job", func(_ *Authenticated, x *Target) { x.NodeID = "sibling" }, apierr.Forbidden},
		{"run", func(_ *Authenticated, x *Target) { x.RunID = "foreign" }, apierr.Forbidden},
		{"grant", func(a *Authenticated, _ *Target) { a.Principal.Capability.Grants = nil }, apierr.Forbidden},
		{"dead", func(a *Authenticated, _ *Target) { a.Live = nil }, apierr.Unauthenticated},
		{"other route", func(a *Authenticated, _ *Target) { a.Pattern = "/other" }, apierr.PolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, x := auth, target
			tc.change(&a, &x)
			_, err := Authorize(context.Background(), bindingCheck(allowBinding), route, a, x)
			requireCode(t, err, tc.code)
		})
	}
	// safety: each alternative must carry its own live evidence, even if grants overlap.
	route.Access = []AccessRule{
		{Credential: CredentialClaim, Grants: []Grant{NodeResult}, Binding: BindOwnNode, Liveness: LiveResult},
		{Credential: CredentialClaim, Grants: []Grant{SourceRead}, Binding: BindSource, Liveness: LiveSensitive},
	}
	auth.Principal.Capability.Grants = []Grant{SourceRead}
	auth.Live = []LiveAccess{{RuleIndex: 0, ResultReplay: true}}
	_, err := Authorize(context.Background(), bindingCheck(allowBinding), route, auth, target)
	requireCode(t, err, apierr.Forbidden)
	_, err = Authorize(context.Background(), nil, route, auth, target)
	requireCode(t, err, apierr.PolicyMissing)
}

func TestDelegateCannotWiden(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	end := now.Add(time.Hour)
	p := workPrincipal()
	p.Capability.ExpiresAt = &end
	p.Capability.Binding.Deadline = &end
	p.Capability.Repositories = []string{"github.com/team/repo"}
	p.Capability.Data = &DataScope{Use: DataClaim, RepositoryKey: "github.com/team/repo", RunID: "run", NodeID: "job", Generation: 2, Attempt: 1, WriteRef: "manual:main", ReadRefs: []string{"manual:main", "main"}}
	child := DelegateSpec{Parent: p.Authority, Audience: NodeHost, Grants: []Grant{DataRead}, Binding: p.Capability.Binding, Repositories: p.Capability.Repositories, Data: p.Capability.Data, ExpiresAt: &end}
	if err := ValidateDelegation(p, child, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*DelegateSpec)
	}{
		{"grant", func(c *DelegateSpec) { c.Grants = []Grant{OperatorConsole} }},
		{"unknown grant", func(c *DelegateSpec) { c.Grants = []Grant{"anything"} }},
		{"audience", func(c *DelegateSpec) { c.Audience = "cache" }},
		{"parent", func(c *DelegateSpec) { c.Parent.ID = "other" }},
		{"unbound", func(c *DelegateSpec) { c.Binding = Binding{Kind: BindingNone} }},
		{"run", func(c *DelegateSpec) { c.Binding.RunID = "other" }},
		{"job", func(c *DelegateSpec) { c.Binding.NodeID = "other" }},
		{"generation", func(c *DelegateSpec) { c.Binding.Generation++ }},
		{"holder", func(c *DelegateSpec) { c.Binding.Holder = "other" }},
		{"repositories", func(c *DelegateSpec) { c.Repositories = []string{"github.com/other/repo"} }},
		{"unlimited expiry", func(c *DelegateSpec) { c.ExpiresAt = nil }},
		{"expiry", func(c *DelegateSpec) { later := end.Add(time.Second); c.ExpiresAt = &later }},
		{"data ref", func(c *DelegateSpec) { data := *c.Data; data.WriteRef = "main"; c.Data = &data }},
		{"data run", func(c *DelegateSpec) { data := *c.Data; data.RunID = "other"; c.Data = &data }},
		{"data use", func(c *DelegateSpec) { data := *c.Data; data.Use = DataPublish; c.Data = &data }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := child
			tc.change(&c)
			requireCode(t, ValidateDelegation(p, c, now), apierr.Forbidden)
		})
	}
	p.Capability.RevokedAt = &now
	requireCode(t, ValidateDelegation(p, child, now), apierr.Unauthenticated)
}

// A push binding is the identity GitHub's token proved for one Actions push, so
// only a credential minted from that proof may pass it on; an unbound credential
// cannot invent one.
func TestOnlyAPushBoundCredentialDelegatesAPush(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	push := Binding{Kind: BindingPush, Push: &PushScope{RepositoryKey: "gh:1", Repository: "acme/repo", Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), WorkflowRunID: "1", WorkflowRunAttempt: "1", WorkflowRef: "acme/repo/.github/workflows/ci.yaml@refs/heads/main"}}
	parent := Principal{Class: CredentialBearer, Authority: AuthorityRef{Kind: AuthorityCapability, ID: "root"}, Capability: Capability{
		Prefix: "root", Team: "team", Subject: Subject{Kind: SubjectMachine, ID: "machine"}, Audience: Controller, Issuance: Delegate,
		Grants: []Grant{RunsWrite}, Binding: Binding{Kind: BindingNone},
	}}
	child := DelegateSpec{Parent: parent.Authority, Audience: Controller, Grants: []Grant{RunsWrite}, Binding: push}
	requireCode(t, ValidateDelegation(parent, child, now), apierr.Forbidden)
	parent.Capability.Binding = push
	if err := ValidateDelegation(parent, child, now); err != nil {
		t.Fatalf("a push-bound credential could not pass on its own push: %v", err)
	}
}

func TestOwnJobInputHasNoAdminBypass(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/runs/{run}/local-host/nodes/{job}/data-authority"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			route, ok := LookupRoute(tc.method, tc.path)
			if !ok {
				t.Fatal("missing policy")
			}
			p := workPrincipal()
			target := Target{Team: "team", RunID: "run", NodeID: "job", SlotRunID: "run", SlotNodeID: "job", SlotGeneration: 2}
			auth := Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []LiveAccess{{RuleIndex: 0}}}
			if _, err := Authorize(context.Background(), bindingCheck(allowBinding), route, auth, target); err != nil {
				t.Fatal(err)
			}
			target.NodeID, target.SlotNodeID = "sibling", "sibling"
			_, err := Authorize(context.Background(), bindingCheck(allowBinding), route, auth, target)
			requireCode(t, err, apierr.Forbidden)
			auth.Principal.Capability.Grants = Grants()
			auth.Principal.Capability.Subject.Kind = SubjectOperator
			auth.Principal.Capability.Unit = ""
			_, err = Authorize(context.Background(), bindingCheck(allowBinding), route, auth, target)
			requireCode(t, err, apierr.Forbidden)
		})
	}
}

func TestRunnerEnrollmentRouteRequiresStoreOriginAuthority(t *testing.T) {
	route, ok := LookupRoute("POST", "/api/v1/runners")
	if !ok {
		t.Fatal("runner enrollment route missing")
	}
	for _, tc := range []struct {
		name    string
		class   CredentialClass
		grants  []Grant
		allowed bool
	}{
		{"read-only session", CredentialSession, []Grant{RunsRead}, false},
		{"personal session", CredentialSession, []Grant{RunsWrite}, true},
		{"admin session", CredentialSession, []Grant{TeamAdmin}, true},
		{"member token", CredentialBearer, []Grant{RunsRead, RunsWrite, TeamAdmin}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind := AuthoritySession
			if tc.class == CredentialBearer {
				kind = AuthorityCapability
			}
			p := Principal{Class: tc.class, Authority: AuthorityRef{Kind: kind, ID: "member"}, Capability: Capability{Team: "team", Audience: Controller, Subject: Subject{Kind: SubjectAccount, ID: "member"}, Grants: tc.grants, Binding: Binding{Kind: BindingNone}}}
			auth := Authenticated{Principal: p, Method: route.Method, Pattern: route.Path}
			for i := range route.Access {
				auth.Live = append(auth.Live, LiveAccess{RuleIndex: i})
			}
			_, err := Authorize(t.Context(), bindingCheck(allowBinding), route, auth, Target{Team: "team"})
			if (err == nil) != tc.allowed {
				t.Fatalf("enrollment allowed=%t, want %t: %v", err == nil, tc.allowed, err)
			}
			if tc.class == CredentialBearer {
				var failure *apierr.Error
				if !errors.As(err, &failure) || !strings.Contains(failure.Message, "--session-stdin") || !strings.Contains(failure.Message, "sparkwing-controller admin session") {
					t.Fatalf("enrollment refusal lacks the headless recovery path: %v", err)
				}
			}
		})
	}
}
