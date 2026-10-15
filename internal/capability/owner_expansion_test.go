package capability_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func ownerExpansionDelegation() (capability.Principal, capability.DelegateSpec) {
	parent := capability.Principal{
		Class:     capability.CredentialBearer,
		Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"},
		Capability: capability.Capability{
			Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "host"},
			Prefix:  "owner", Audience: capability.Controller, Team: "team", Unit: capability.UnitTrigger, UnitID: "trigger",
			Purpose: capability.PurposeTrigger, Issuance: capability.Award, Grants: []capability.Grant{capability.ExpansionSubmit, capability.DataRead, capability.NodeResult},
			Binding: capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Generation: 9, Holder: "host"},
			Data:    &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "github.com/team/repo", RunID: "run", Generation: 9, WriteRef: "manual:main", ReadRefs: []string{"main", "manual:main"}, VerifiedDefaultOnly: true},
		},
	}
	child := capability.DelegateSpec{
		Parent: parent.Authority, Audience: capability.Controller,
		Grants: []capability.Grant{capability.ExpansionSubmit, capability.DataRead},
		Binding: capability.Binding{
			Kind: capability.BindingNode, RunID: "run", NodeID: "source", Generation: 4, Holder: "source-runner", Attempt: 2,
			Expansion: &capability.LeaseRef{Kind: capability.UnitTrigger, ID: "trigger", Generation: 9, Holder: "host"},
		},
		Data: &capability.DataScope{Use: capability.DataExpansion, RepositoryKey: "github.com/team/repo", RunID: "run", NodeID: "source", Generation: 4, Attempt: 2, ReadRefs: []string{"main"}, VerifiedDefaultOnly: true},
	}
	return parent, child
}

func TestOwnerExpansionDataIsReadOnly(t *testing.T) {
	parent, child := ownerExpansionDelegation()
	principal := parent
	principal.Capability.Issuance = capability.Delegate
	principal.Capability.Binding, principal.Capability.Data, principal.Capability.Grants = child.Binding, child.Data, child.Grants
	a := capability.Authorized{
		Principal: principal, Binding: child.Binding, Target: capability.Target{Team: "team"},
		Access: capability.AccessRule{Grants: []capability.Grant{capability.DataRead}, Binding: capability.BindData, Liveness: capability.LiveExpansion},
	}
	calls := 0
	edges := dataCheck(func(context.Context, capability.Authorized, capability.DataTarget) error { calls++; return nil })
	if err := capability.AuthorizeData(t.Context(), edges, a, capability.DataTarget{Operation: capability.DataDownload, Digest: "source-output"}); err != nil || calls != 1 {
		t.Fatalf("source data read: calls=%d, err=%v", calls, err)
	}
	if err := capability.AuthorizeData(t.Context(), edges, a, capability.DataTarget{Operation: capability.DataUpload}); err == nil || calls != 1 {
		t.Fatalf("expansion data write: calls=%d, err=%v", calls, err)
	}
}

func TestOwnerDelegatesOnlySourceExpansionAuthority(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	parent, child := ownerExpansionDelegation()
	if err := capability.ValidateDelegation(parent, child, now); err != nil {
		t.Fatalf("source expansion narrowing refused: %v", err)
	}
	for _, name := range []string{"another run", "another lease", "another holder", "another generation", "eval lease", "nodehost expansion", "missing source", "missing source generation", "added reporting", "added discovery", "added source access", "without expansion grant", "unbound root", "work claim", "expired owner", "later deadline", "later claim window", "lost reservation", "source widening", "foreign data repo", "extra ref", "data writes", "claim data", "wrong data source", "wrong data generation", "wrong data attempt", "lost default guard", "parent data restricted"} {
		t.Run(name, func(t *testing.T) {
			parent, child := ownerExpansionDelegation()
			switch name {
			case "another run":
				child.Binding.RunID = "foreign"
			case "another lease":
				child.Binding.Expansion.ID = "foreign"
			case "another holder":
				child.Binding.Expansion.Holder = "foreign"
			case "another generation":
				child.Binding.Expansion.Generation++
			case "eval lease":
				child.Binding.Expansion.Kind = capability.UnitEval
			case "nodehost expansion":
				child.Audience = capability.NodeHost
			case "missing source":
				child.Binding.NodeID = ""
			case "missing source generation":
				child.Binding.Generation = 0
			case "added reporting":
				child.Grants = append(child.Grants, capability.NodeResult)
			case "added discovery":
				parent.Capability.Grants = append(parent.Capability.Grants, capability.RunsRead)
				child.Grants = append(child.Grants, capability.RunsRead)
			case "added source access":
				parent.Capability.Grants = append(parent.Capability.Grants, capability.SourceRead)
				child.Grants = append(child.Grants, capability.SourceRead)
			case "without expansion grant":
				parent.Capability.Grants = []capability.Grant{capability.DataRead}
				child.Grants = []capability.Grant{capability.DataRead}
			case "unbound root":
				parent.Capability.Binding.Kind = capability.BindingNone
			case "work claim":
				parent.Capability.Unit, parent.Capability.Purpose = capability.UnitNode, capability.PurposeWork
			case "expired owner":
				parent.Capability.ExpiresAt = &now
			case "later deadline":
				ceiling, later := now.Add(time.Minute), now.Add(2*time.Minute)
				parent.Capability.Binding.Deadline, child.Binding.Deadline = &ceiling, &later
			case "later claim window":
				window := now.Add(time.Minute)
				parent.Capability.Binding.ClaimBy = &window
			case "lost reservation":
				parent.Capability.Binding.Reservation = "here:run"
			case "source widening":
				parent.Capability.Binding = child.Binding
				child.Binding.NodeID = "sibling"
			case "foreign data repo":
				child.Data.RepositoryKey = "github.com/foreign/repo"
			case "extra ref":
				child.Data.ReadRefs = []string{"ungranted"}
			case "data writes":
				child.Data.WriteRef = "manual:main"
			case "claim data":
				parent.Capability.Data.Generation = 0
				child.Data.Use = capability.DataClaim
			case "wrong data source":
				child.Data.NodeID = "sibling"
			case "wrong data generation":
				child.Data.Generation++
			case "wrong data attempt":
				child.Data.Attempt++
			case "lost default guard":
				child.Data.VerifiedDefaultOnly = false
			case "parent data restricted":
				parent.Capability.Data.NodeID = "sibling"
			}
			if err := capability.ValidateDelegation(parent, child, now); err == nil {
				t.Fatal("widened source expansion delegation accepted")
			}
		})
	}
	parent, child = ownerExpansionDelegation()
	window := now.Add(time.Minute)
	parent.Capability.Purpose = capability.PurposeHere
	parent.Capability.Binding.Reservation, child.Binding.Reservation = "here:run", "here:run"
	parent.Capability.Binding.ClaimBy, child.Binding.ClaimBy = &window, &window
	if err := capability.ValidateDelegation(parent, child, now); err != nil {
		t.Fatalf("claimed here owner expansion refused: %v", err)
	}
}
