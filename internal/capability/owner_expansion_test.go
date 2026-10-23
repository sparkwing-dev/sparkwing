package capability

import (
	"context"
	"testing"
	"time"
)

func ownerExpansionDelegation() (Principal, DelegateSpec) {
	parent := Principal{
		Class:     CredentialBearer,
		Authority: AuthorityRef{Kind: AuthorityCapability, ID: "owner"},
		Capability: Capability{
			Subject: Subject{Kind: SubjectMachine, ID: "host"},
			Prefix:  "owner", Audience: Controller, Team: "team", Unit: UnitTrigger, UnitID: "trigger",
			Purpose: PurposeTrigger, Issuance: Award, Grants: []Grant{ExpansionSubmit, DataRead, NodeResult},
			Binding: Binding{Kind: BindingTrigger, RunID: "run", Generation: 9, Holder: "host"},
			Data:    &DataScope{Use: DataClaim, RepositoryKey: "github.com/team/repo", RunID: "run", Generation: 9, WriteRef: "manual:main", ReadRefs: []string{"main", "manual:main"}, VerifiedDefaultOnly: true},
		},
	}
	child := DelegateSpec{
		Parent: parent.Authority, Audience: Controller,
		Grants: []Grant{ExpansionSubmit, DataRead},
		Binding: Binding{
			Kind: BindingNode, RunID: "run", NodeID: "source", Generation: 4, Holder: "source-runner", Attempt: 2,
			Expansion: &LeaseRef{Kind: UnitTrigger, ID: "trigger", Generation: 9, Holder: "host"},
		},
		Data: &DataScope{Use: DataExpansion, RepositoryKey: "github.com/team/repo", RunID: "run", NodeID: "source", Generation: 4, Attempt: 2, ReadRefs: []string{"main"}, VerifiedDefaultOnly: true},
	}
	return parent, child
}

func TestOwnerExpansionDataIsReadOnly(t *testing.T) {
	parent, child := ownerExpansionDelegation()
	principal := parent
	principal.Capability.Issuance = Delegate
	principal.Capability.Binding, principal.Capability.Data, principal.Capability.Grants = child.Binding, child.Data, child.Grants
	a := Authorized{
		Principal: principal, Binding: child.Binding, Target: Target{Team: "team"},
		Access: AccessRule{Grants: []Grant{DataRead}, Binding: BindData, Liveness: LiveExpansion},
	}
	calls := 0
	edges := dataCheck(func(context.Context, Authorized, DataTarget) error { calls++; return nil })
	if err := AuthorizeData(t.Context(), edges, a, DataTarget{Operation: DataDownload, Digest: "source-output"}); err != nil || calls != 1 {
		t.Fatalf("source data read: calls=%d, err=%v", calls, err)
	}
	if err := AuthorizeData(t.Context(), edges, a, DataTarget{Operation: DataUpload}); err == nil || calls != 1 {
		t.Fatalf("expansion data write: calls=%d, err=%v", calls, err)
	}
}

func TestOwnerDelegatesOnlySourceExpansionAuthority(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	parent, child := ownerExpansionDelegation()
	if err := ValidateDelegation(parent, child, now); err != nil {
		t.Fatalf("source expansion narrowing refused: %v", err)
	}
	for _, name := range []string{"another run", "another lease", "another holder", "another generation", "eval lease", "nodehost expansion", "missing source", "negative source generation", "added reporting", "added discovery", "added source access", "without expansion grant", "unbound root", "work claim", "expired owner", "later deadline", "later claim window", "lost reservation", "source widening", "foreign data repo", "extra ref", "data writes", "claim data", "wrong data source", "wrong data generation", "wrong data attempt", "lost default guard", "parent data restricted"} {
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
				child.Binding.Expansion.Kind = UnitEval
			case "nodehost expansion":
				child.Audience = NodeHost
			case "missing source":
				child.Binding.NodeID = ""
			case "negative source generation":
				child.Binding.Generation = -1
			case "added reporting":
				child.Grants = append(child.Grants, NodeResult)
			case "added discovery":
				parent.Capability.Grants = append(parent.Capability.Grants, RunsRead)
				child.Grants = append(child.Grants, RunsRead)
			case "added source access":
				parent.Capability.Grants = append(parent.Capability.Grants, SourceRead)
				child.Grants = append(child.Grants, SourceRead)
			case "without expansion grant":
				parent.Capability.Grants = []Grant{DataRead}
				child.Grants = []Grant{DataRead}
			case "unbound root":
				parent.Capability.Binding.Kind = BindingNone
			case "work claim":
				parent.Capability.Unit, parent.Capability.Purpose = UnitNode, PurposeWork
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
				child.Data.Use = DataClaim
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
			if err := ValidateDelegation(parent, child, now); err == nil {
				t.Fatal("widened source expansion delegation accepted")
			}
		})
	}
	parent, child = ownerExpansionDelegation()
	window := now.Add(time.Minute)
	parent.Capability.Purpose = PurposeHere
	parent.Capability.Binding.Reservation, child.Binding.Reservation = "here:run", "here:run"
	parent.Capability.Binding.ClaimBy, child.Binding.ClaimBy = &window, &window
	if err := ValidateDelegation(parent, child, now); err != nil {
		t.Fatalf("claimed here owner expansion refused: %v", err)
	}
}

func FuzzOwnerExpansionKeepsSourceAndOwnerFencesSeparate(f *testing.F) {
	for _, generation := range []int64{-1, 0, 1, 4} {
		f.Add(generation, int64(9), false)
		f.Add(generation, int64(0), false)
		f.Add(generation, int64(9), true)
	}
	f.Fuzz(func(t *testing.T, generation, ownerGeneration int64, widenData bool) {
		parent, child := ownerExpansionDelegation()
		parent.Capability.Binding.Generation = ownerGeneration
		parent.Capability.Data.Generation = ownerGeneration
		child.Binding.Expansion.Generation = ownerGeneration
		child.Binding.Generation, child.Data.Generation = generation, generation
		if widenData {
			child.Data.Generation++
		}
		err := ValidateDelegation(parent, child, time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC))
		want := generation >= 0 && ownerGeneration > 0 && !widenData
		if (err == nil) != want {
			t.Fatalf("source=%d owner=%d widen=%v: err=%v", generation, ownerGeneration, widenData, err)
		}
		if !want {
			return
		}
		parent.Authority.ID = "expansion"
		parent.Capability.Binding, parent.Capability.Data = child.Binding, child.Data
		parent.Capability.Grants = child.Grants
		parent.Capability.Issuance = Delegate
		child.Parent = parent.Authority
		child.Binding.Generation++
		data := *child.Data
		data.Generation++
		child.Data = &data
		if err := ValidateDelegation(parent, child, time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)); err == nil {
			t.Fatal("delegated terminal source generation widened")
		}
	})
}
