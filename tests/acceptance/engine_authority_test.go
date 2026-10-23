package acceptance_test

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func expansionAuthority() capability.Authorized {
	b := newCredentialBoundary()
	p := b.snapshot.Authorities[0].Principal
	p.Capability.Grants = []capability.Grant{capability.ExpansionSubmit}
	p.Capability.Binding.Expansion = &capability.LeaseRef{Kind: capability.UnitEval, ID: "eval", Generation: 9, Holder: "controller"}
	p.Capability.Unit, p.Capability.UnitID, p.Capability.Purpose = capability.UnitEval, "eval", capability.PurposeEval
	return capability.Authorized{
		Principal: p, Binding: p.Capability.Binding,
		Target: capability.Target{Team: "team", RunID: "run", NodeID: "job", UnitSourceNodeID: "job", UnitSourceGeneration: 4},
		Access: capability.AccessRule{Credential: capability.CredentialClaim, Grants: []capability.Grant{capability.ExpansionSubmit}, Binding: capability.BindExpansion, Liveness: capability.LiveExpansion},
	}
}

func TestEngineExpansionAuthorityAdapter(t *testing.T) {
	a := expansionAuthority()
	for _, failed := range []bool{false, true} {
		body := controllerwire.ExpansionSubmission{SourceGeneration: 4, Body: json.RawMessage(`{"jobs":[]}`)}
		if failed {
			body.Body = nil
			body.Failure = &apierr.Error{Code: apierr.Unavailable, Message: "generator failed"}
		}
		got, err := controller.ExpansionForStore(a, body)
		if err != nil {
			t.Fatal(err)
		}
		if got.Source != (runspec.NodeRef{RunID: "run", NodeID: "job"}) || got.SourceGeneration != 4 ||
			got.Actor.Authority != a.Principal.Authority || got.Actor.CapabilityPrefix != "claim" ||
			got.Actor.Owner.Kind != runstate.OwnerEval || got.Actor.Owner.Name != "eval" ||
			got.Actor.Owner.Generation != 9 || got.Actor.Owner.Holder != "controller" ||
			got.Actor.Owner.SourceGeneration != 4 || got.Actor.Owner.SourceNodeID != "job" {
			t.Fatalf("authority was lost: %+v", got)
		}
		if failed {
			if got.Failure == nil || got.Failure.Code != apierr.Unavailable || got.Failure.MaskedError != "generator failed" || len(got.Body) != 0 {
				t.Fatalf("structured failure = %+v", got)
			}
		} else if got.Failure != nil || string(got.Body) != `{"jobs":[]}` {
			t.Fatalf("explicit empty expansion = %+v", got)
		}
	}
	for _, body := range []controllerwire.ExpansionSubmission{
		{SourceGeneration: 4},
		{SourceGeneration: 4, Body: json.RawMessage(`null`)},
		{SourceGeneration: 4, Body: json.RawMessage(`{"jobs":null}`)},
		{SourceGeneration: 4, Body: json.RawMessage(`{}`)},
		{SourceGeneration: 4, Body: json.RawMessage(`[]`)},
		{SourceGeneration: 4, Body: json.RawMessage(`{"jobs":[]} {}`)},
		{SourceGeneration: 4, Body: json.RawMessage(`{"jobs":[]}`), Failure: &apierr.Error{Code: apierr.Internal, Message: "failed"}},
		{SourceGeneration: 4, Failure: &apierr.Error{Message: "missing code"}},
	} {
		if _, err := controller.ExpansionForStore(a, body); err == nil {
			t.Fatalf("invalid expansion accepted: %+v", body)
		}
	}
	if _, err := controller.ExpansionForStore(a, controllerwire.ExpansionSubmission{SourceGeneration: 3, Body: json.RawMessage(`{"jobs":[]}`)}); err == nil {
		t.Fatal("stale source generation was accepted")
	}
}

func TestEngineIntakeKeepsTaggedAuthority(t *testing.T) {
	for _, class := range []capability.CredentialClass{capability.CredentialBearer, capability.CredentialSession} {
		a := expansionAuthority()
		a.Principal.Class = class
		a.Principal.Capability.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "account"}
		a.Principal.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "member"}
		if class == capability.CredentialSession {
			a.Principal.Authority = capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "session"}
			a.Principal.Capability.Prefix = ""
		}
		body := controllerwire.CreateRunRequest{IdempotencyKey: "same-intake", Request: runspec.Request{
			Team: "team", Pipeline: "build", Actor: runspec.Actor{Kind: "operator", ID: "spoofed"},
			Git: runspec.Git{App: runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{DeliveryKey: "unverified", DefaultBranchPush: true}}},
		}}
		got, err := controller.IntakeForStore(a, body)
		if err != nil {
			t.Fatal(err)
		}
		if got.Authority != a.Principal.Authority || got.IdempotencyKey != body.IdempotencyKey || got.Request.Team != "team" ||
			got.Request.Actor.ID != "account" || got.Request.Actor.Kind != "account" || got.Request.Git.App.Set || got.VerifiedDeliveryID != "" {
			t.Fatalf("intake authority = %+v", got)
		}
		wantPrefix := "claim"
		if class == capability.CredentialSession {
			wantPrefix = ""
		}
		if got.CapabilityPrefix != wantPrefix {
			t.Fatalf("claim prefix = %q, want %q", got.CapabilityPrefix, wantPrefix)
		}
		if body.Request.Git.App.Value.DeliveryKey != "unverified" {
			t.Fatal("adapter changed caller input")
		}
	}
}
