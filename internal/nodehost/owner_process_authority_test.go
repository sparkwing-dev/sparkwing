package nodehost

import (
	"context"
	"crypto/sha256"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func ownerProcessAuthorityFixture(t *testing.T, evaluation bool) (*ProcessAuthorities, *processParent, capability.CredentialLookup, capability.DelegateSpec, *ProcessServices) {
	t.Helper()
	registry, parent, lookup, spec, service := processAuthorityFixture(t)
	registry.deps.ValidateAuthority = capability.ValidateAuthority
	registry.deps.ValidateDelegation = capability.ValidateDelegation
	principal := &parent.snapshot.Authorities[0].Principal
	principal.Capability.Unit, principal.Capability.UnitID = capability.UnitTrigger, "owner"
	principal.Capability.Purpose, principal.Capability.Issuance = capability.PurposeTrigger, capability.Award
	principal.Capability.Binding.Kind, principal.Capability.Binding.NodeID = capability.BindingTrigger, ""
	principal.Capability.Binding.Attempt = 0
	principal.Capability.Grants = []capability.Grant{capability.DataRead, capability.NodeReport}
	if evaluation {
		principal.Capability.Unit, principal.Capability.UnitID = capability.UnitEval, "eval"
		principal.Capability.Purpose = capability.PurposeEval
		principal.Capability.Binding.Kind, principal.Capability.Binding.NodeID = capability.BindingNode, "job"
		principal.Capability.Binding.Generation = 0
		principal.Capability.Binding.Expansion = &capability.LeaseRef{Kind: capability.UnitEval, ID: "eval", Generation: 9, Holder: "evaluator"}
		principal.Capability.Grants = []capability.Grant{capability.ExpansionSubmit, capability.DataRead}
		parent.snapshot.Binding.FinishedAt = &parent.snapshot.Now
		expires := parent.snapshot.Now.Add(time.Hour)
		parent.snapshot.Binding.Expansion = &capability.ExpansionState{Lease: *principal.Capability.Binding.Expansion, ExpiresAt: expires, ControllerLeaseExpiresAt: &expires}
	}
	service.SourceBindings = registry.deps.Bindings
	spec.Binding, spec.Grants = principal.Capability.Binding, principal.Capability.Grants
	parent.snapshot.Binding.Current = spec.Binding
	parent.snapshot.Authorities[0].Principal.Capability.Digest = sha256.Sum256([]byte("parent-token"))
	return registry, parent, lookup, spec, service
}

func TestOwnerProcessKeepsOwnerLeaseAndSeparateSDKJob(t *testing.T) {
	for _, evaluation := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner", true: "eval"}[evaluation], func(t *testing.T) {
			registry, parent, lookup, spec, service := ownerProcessAuthorityFixture(t, evaluation)
			issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service)
			if err != nil {
				t.Fatal(err)
			}
			defer revoke()
			if !reflect.DeepEqual(issued.Capability.Binding, spec.Binding) {
				t.Fatal("evaluation changed its owner fence")
			}
			route, _ := capability.LookupRoute("GET", "/node/v1/jobs/{job}/output")
			authenticated, err := capability.NewAuthenticator(registry).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialBearer, Raw: issued.Raw}, route)
			if err != nil {
				t.Fatal(err)
			}
			target := capability.Target{Team: "team", RunID: "run", NodeID: "upstream"}
			if evaluation {
				target.NodeID = "job"
			}
			authorized, err := capability.Authorize(t.Context(), registry, route, authenticated, target)
			if err != nil {
				t.Fatal(err)
			}
			bound, err := registry.Resolve(t.Context(), authorized)
			if err != nil || bound.Job.ID != "job" {
				t.Fatalf("source job lost: %+v %v", bound, err)
			}
			if !evaluation && parent.snapshot.Binding.Current.Attempt != 0 {
				t.Fatal("owner evaluation invented an attempt")
			}
			if err := revoke(); err != nil {
				t.Fatal(err)
			}
			if _, err := capability.NewAuthenticator(registry).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialBearer, Raw: issued.Raw}, route); err == nil {
				t.Fatal("revoked process retained authority")
			}
		})
	}
}

func FuzzOwnerProcessCannotOutliveItsAuthority(f *testing.F) {
	for i := uint8(0); i < 8; i++ {
		f.Add(i, false, false)
		f.Add(i, true, false)
		f.Add(i, false, true)
		f.Add(i, true, true)
	}
	f.Fuzz(func(t *testing.T, change uint8, evaluation, auxiliary bool) {
		registry, parent, lookup, spec, service := ownerProcessAuthorityFixture(t, evaluation)
		issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service)
		if err != nil {
			t.Fatal(err)
		}
		defer revoke()
		route, _ := capability.LookupRoute("GET", "/node/v1/jobs/{job}/output")
		if auxiliary {
			route, _ = capability.LookupRoute("POST", "/node/v1/masks")
		}
		authenticated, err := capability.NewAuthenticator(registry).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialBearer, Raw: issued.Raw}, route)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := capability.Authorize(t.Context(), registry, route, authenticated, capability.Target{Team: "team", RunID: "run", NodeID: "job"}); err != nil {
			t.Fatal(err)
		}
		switch change % 8 {
		case 0:
			parent.snapshot.Binding.RunCancelled = true
		case 1:
			parent.snapshot.Binding.RunFinished = true
		case 2:
			parent.snapshot.Authorities[0].MembershipValid = false
		case 3:
			parent.snapshot.Authorities[0].Principal.Capability.RevokedAt = &parent.snapshot.Now
		case 4:
			parent.snapshot.Binding.LeaseExpiresAt = parent.snapshot.Now
			if evaluation {
				parent.snapshot.Binding.Expansion.ExpiresAt = parent.snapshot.Now
			}
		case 5:
			parent.snapshot.Binding.Current.Generation++
		case 6:
			if evaluation {
				parent.snapshot.Binding.Expansion.Lease.Holder += "other"
			} else {
				parent.snapshot.Binding.Current.Holder += "other"
			}
		case 7:
			if err := revoke(); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := capability.NewAuthenticator(registry).Authenticate(context.Background(), capability.Presentation{Class: capability.CredentialBearer, Raw: issued.Raw}, route); err == nil {
			t.Fatalf("authority change %d left process live", change%8)
		}
	})
}

func TestEvaluationAuxiliaryServicesKeepSourceLease(t *testing.T) {
	for _, operation := range []struct{ method, path string }{{"POST", "/node/v1/masks"}, {"POST", "/node/v1/annotations"}, {"POST", "/node/v1/summary"}, {"POST", "/node/v1/cleanups"}, {"DELETE", "/node/v1/cleanups/{cleanup}"}} {
		t.Run(operation.path+operation.method, func(t *testing.T) {
			registry, parent, lookup, spec, service := ownerProcessAuthorityFixture(t, true)
			parent.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.ExpansionSubmit, capability.DataRead}
			spec.Grants = parent.snapshot.Authorities[0].Principal.Capability.Grants
			service.Cleanups = &ProcessCleanups{}
			cleanup, err := service.Cleanups.Register([]string{"true"}, "")
			if err != nil {
				t.Fatal(err)
			}
			issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service)
			if err != nil {
				t.Fatal(err)
			}
			defer revoke()
			route, _ := capability.LookupRoute(operation.method, operation.path)
			authenticated, err := capability.NewAuthenticator(registry).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialBearer, Raw: issued.Raw}, route)
			if err != nil {
				t.Fatal(err)
			}
			target := capability.Target{Team: "team", RunID: "run", NodeID: "job"}
			if operation.method == "DELETE" {
				target.CleanupID = cleanup
			}
			authorized, err := capability.Authorize(t.Context(), registry, route, authenticated, target)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(authorized.Binding, spec.Binding) || authorized.Binding.Attempt != 0 {
				t.Fatal("evaluation auxiliary service lost source lease")
			}
			if operation.method == "DELETE" {
				target.CleanupID += "other"
				if _, err := capability.Authorize(t.Context(), registry, route, authenticated, target); err == nil {
					t.Fatal("evaluation released foreign cleanup")
				}
			}
		})
	}
}
