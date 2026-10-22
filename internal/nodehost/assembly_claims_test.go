package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func localClaimsFixture(t testing.TB) (*nodeClaims, *nativeAuthentication, store.ReconciledNode, *bool) {
	t.Helper()
	now := time.Unix(10_000, 0)
	raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	owner := capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}, Capability: capability.Capability{Prefix: "owner", Team: "default", Subject: capability.Subject{Kind: capability.SubjectLocal, ID: "account"}, Binding: capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Holder: "account", Generation: 1}}}
	p := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "node"}, Capability: capability.Capability{
		Prefix: "node", Digest: sha256.Sum256([]byte(raw)), Audience: capability.Controller, Team: "default", Subject: owner.Capability.Subject,
		Issuance: capability.Award, Unit: capability.UnitNode, UnitID: "node", Purpose: capability.PurposeWork, Delivery: capability.DeliveryDirect, Grants: []capability.Grant{capability.NodeReport, capability.DataRead}, Data: &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "repo", RunID: "run", NodeID: "job", Generation: 2}, IssuedAt: now.Add(-time.Hour),
		Binding: capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "account", Generation: 2},
	}}
	state := &nativeAuthentication{snapshot: capability.AuthenticationSnapshot{Now: now, Authorities: []capability.AuthorityState{{Principal: p}}, Binding: capability.BindingState{Current: p.Capability.Binding, LeaseExpiresAt: now.Add(time.Minute)}}}
	lost := false
	claims := &nodeClaims{services: completeServiceFixture, auth: capability.NewAuthenticator(state), bindings: state, masker: secrets.NewMasker(), current: func(ctx context.Context) (capability.Principal, error) {
		if lost {
			return capability.Principal{}, errors.New("owner lost")
		}
		return owner, context.Cause(ctx)
	}}
	award := store.ReconciledNode{Kind: store.Execute, Node: store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, CapabilityPrefix: "node", OwnerCapabilityPrefix: "owner", ClaimedBy: "account", Generation: 2}, Claim: &store.ClaimAward{CapabilityPrefix: "node", Holder: "account", Generation: 2, LeaseExpiresAt: state.snapshot.Binding.LeaseExpiresAt}, Bearer: []byte(raw)}
	return claims, state, award, &lost
}

func TestLocalOwnerAdoptsIndependentNodeClaim(t *testing.T) {
	claims, state, award, _ := localClaimsFixture(t)
	if _, err := claims.principal(t.Context(), "job", 2, 0); err == nil {
		t.Fatal("owner authority substituted for missing node award")
	}
	if err := claims.adopt(t.Context(), award); err != nil {
		t.Fatal(err)
	}
	if claims.masker.Mask(string(award.Bearer)) == string(award.Bearer) {
		t.Fatal("private node bearer was not masked")
	}
	award.Bearer[0] = '!'
	for attempt := int64(1); attempt <= 3; attempt++ {
		state.snapshot.Binding.Current.Attempt = attempt
		p, err := claims.principal(t.Context(), "job", 2, attempt)
		if err != nil || p.Authority.ID != "node" || p.Capability.Binding.Attempt != attempt {
			t.Fatalf("attempt %d lost its independent award: %+v: %v", attempt, p, err)
		}
	}
}

func FuzzLocalNodeAwardFence(f *testing.F) {
	for i := uint8(0); i < 14; i++ {
		f.Add(i, uint32(1))
	}
	f.Fuzz(func(t *testing.T, mutation uint8, delta uint32) {
		claims, state, award, lost := localClaimsFixture(t)
		if err := claims.adopt(t.Context(), award); err != nil {
			t.Fatal(err)
		}
		generation, attempt, node := int64(2), int64(0), "job"
		switch mutation % 14 {
		case 0:
			generation += int64(delta) + 1
		case 1:
			state.snapshot.Authorities[0].Principal.Capability.Binding.Attempt = int64(delta) + 1
			state.snapshot.Binding.Current.Attempt = int64(delta) + 1
		case 2:
			node += "-other"
		case 3:
			state.snapshot.Binding.Current.Generation += int64(delta) + 1
		case 4:
			state.snapshot.Authorities[0].Principal.Capability.Binding.Attempt = 1
			state.snapshot.Binding.Current.Attempt = int64(delta) + 2
		case 5:
			state.snapshot.Binding.Current.Holder += "-other"
		case 6:
			state.snapshot.Binding.LeaseExpiresAt = state.snapshot.Now
		case 7:
			state.snapshot.Binding.FinishedAt = &state.snapshot.Now
		case 8:
			state.snapshot.Binding.RunFinished = true
		case 9:
			state.snapshot.Binding.RunCancelled = true
		case 10:
			state.snapshot.Authorities[0].Principal.Capability.RevokedAt = &state.snapshot.Now
		case 11:
			state.snapshot.Authorities[0].Principal.Capability.RevokedAt = &state.snapshot.Now
		case 12:
			*lost = true
		case 13:
			state.snapshot.Authorities[0].Principal.Capability.Subject.ID += "-other"
		}
		if p, err := claims.principal(t.Context(), node, generation, attempt); err == nil || p.Authority.ID != "" {
			t.Fatalf("lost node fence reached delegation: %+v: %v", p, err)
		}
	})
}

func FuzzLocalNodeAdoptionRejectsForeignAwards(f *testing.F) {
	for i := uint8(0); i < 10; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, mutation uint8) {
		claims, _, award, _ := localClaimsFixture(t)
		switch mutation % 10 {
		case 0:
			award.Kind = store.WaitHolder
		case 1:
			award.Node.Ref.RunID += "-other"
		case 2:
			award.Node.Ref.NodeID += "-other"
		case 3:
			award.Node.OwnerCapabilityPrefix += "-other"
		case 4:
			award.Node.Generation++
		case 5:
			award.Node.ClaimedBy += "-other"
		case 6:
			award.Node.CapabilityPrefix += "-other"
		case 7:
			award.Claim = nil
		case 8:
			award.Bearer = nil
		case 9:
			award.Claim.Generation++
		}
		if err := claims.adopt(t.Context(), award); err == nil {
			t.Fatal("foreign award adopted")
		}
		if _, err := claims.principal(t.Context(), "job", 2, 0); err == nil {
			t.Fatal("failed adoption retained node authority")
		}
	})
}

func TestLocalAdoptedClaimDelegatesMatchingSDKProcess(t *testing.T) {
	claims, state, award, _ := localClaimsFixture(t)
	if err := claims.adopt(t.Context(), award); err != nil {
		t.Fatal(err)
	}
	state.snapshot.Binding.Current.Attempt = 1
	state.snapshot.Binding.StartPermitted = true
	authorities, err := NewProcessAuthorities(ProcessAuthorityDependencies{Parent: state, Bindings: state})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := authorities.Close(); err != nil {
			t.Error(err)
		}
	})
	processes := &NativeProcesses{Authorities: authorities, Masks: NewMaskRegistry(claims.masker), URL: "http://host"}
	row := award.Node
	row.Attempt = 1
	row.Spec, err = json.Marshal(nodeproto.JobSpec{ID: string(row.Ref.NodeID)})
	if err != nil {
		t.Fatal(err)
	}
	claims.readNode = func(context.Context, controllerwire.NodeRef) (store.NodeRow, error) { return row, nil }
	claims.processes = processes
	grant, err := claims.delegate(t.Context(), string(row.Ref.NodeID), int64(row.Generation), int64(row.Attempt))
	if err != nil {
		t.Fatal(err)
	}
	lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.NodeHost, Digest: sha256.Sum256([]byte(grant.Binding.Routes.Bearer))}
	snapshot, err := authorities.ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{})
	if err != nil || len(snapshot.Authorities) != 2 {
		t.Fatalf("process ancestry: %+v: %v", snapshot, err)
	}
	p := snapshot.Authorities[0].Principal
	if p.Capability.Parent == nil || p.Capability.Parent.ID != row.CapabilityPrefix || p.Capability.Binding != state.snapshot.Binding.Current {
		t.Fatalf("SDK process used another authority: %+v", p)
	}
	if p.Capability.Data == nil || runspec.AttemptOrdinal(p.Capability.Data.Attempt) != row.Attempt || state.snapshot.Authorities[0].Principal.Capability.Data.Attempt != 0 {
		t.Fatal("process data scope did not narrow the immutable claim to its attempt")
	}
	if err := grant.Revoke(); err != nil {
		t.Fatal(err)
	}
	if _, err := authorities.ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{}); err == nil {
		t.Fatal("joined process kept its delegated authority")
	}
}

func FuzzLocalProcessDelegationFence(f *testing.F) {
	for i := uint8(0); i < 7; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, mutation uint8) {
		claims, state, award, _ := localClaimsFixture(t)
		if err := claims.adopt(t.Context(), award); err != nil {
			t.Fatal(err)
		}
		state.snapshot.Binding.Current.Attempt = 1
		authorities, err := NewProcessAuthorities(ProcessAuthorityDependencies{Parent: state, Bindings: state})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := authorities.Close(); err != nil {
				t.Error(err)
			}
		})
		processes := &NativeProcesses{Authorities: authorities, Masks: NewMaskRegistry(claims.masker), URL: "http://host"}
		row := award.Node
		row.Attempt, row.Spec = 1, json.RawMessage(`{"id":"job"}`)
		switch mutation % 7 {
		case 0:
			row.Ref.RunID += "-other"
		case 1:
			row.Ref.NodeID += "-other"
		case 2:
			row.Generation++
		case 3:
			row.Attempt++
		case 4:
			row.ClaimedBy += "-other"
		case 5:
			row.CapabilityPrefix += "-other"
		case 6:
			row.Spec = json.RawMessage(`{"id":"other"}`)
		}
		claims.readNode = func(context.Context, controllerwire.NodeRef) (store.NodeRow, error) { return row, nil }
		claims.processes = processes
		grant, err := claims.delegate(t.Context(), "job", 2, 1)
		if err == nil || grant.Binding.Routes.Bearer != "" || grant.Revoke != nil {
			t.Fatalf("foreign attempt received SDK authority: %+v: %v", grant.Binding, err)
		}
	})
}

func TestLocalNodeClaimsConcurrentAdoptionAndBinding(t *testing.T) {
	claims, _, award, _ := localClaimsFixture(t)
	var workers sync.WaitGroup
	results := make(chan error, 32)
	for range cap(results) {
		workers.Go(func() {
			err := claims.adopt(t.Context(), award)
			if err == nil {
				_, err = claims.principal(t.Context(), "job", 2, 0)
			}
			results <- err
		})
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestLocalOwnerDelegatesEvaluationBeforeAnyNodeAward(t *testing.T) {
	claims, state, award, _ := localClaimsFixture(t)
	owner, _ := claims.current(t.Context())
	owner.Class = capability.CredentialBearer
	owner.Capability.Audience = capability.Controller
	owner.Capability.Unit, owner.Capability.UnitID = capability.UnitTrigger, "owner-unit"
	owner.Capability.Purpose, owner.Capability.Issuance = capability.PurposeTrigger, capability.Award
	owner.Capability.Digest = sha256.Sum256([]byte("owner-token"))
	owner.Capability.Grants = []capability.Grant{capability.DataRead, capability.NodeReport}
	owner.Capability.Binding.Attempt = 0
	state.snapshot.Authorities[0].Principal = owner
	state.snapshot.Binding.Current = owner.Capability.Binding
	claims.current = func(context.Context) (capability.Principal, error) { return owner, nil }
	registry, err := NewProcessAuthorities(ProcessAuthorityDependencies{Parent: state, Bindings: state})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	})
	claims.processes = &NativeProcesses{Authorities: registry, Masks: NewMaskRegistry(claims.masker), URL: "http://host"}
	source := award.Node
	source.Generation, source.Attempt = 0, 0
	source.CapabilityPrefix, source.ClaimedBy = "", ""
	source.SpecHash = "accepted-job-hash"
	source.Spec = []byte(`{"id":"job"}`)
	claims.readNode = func(context.Context, controllerwire.NodeRef) (store.NodeRow, error) { return source, nil }
	input := EvaluationInput{Accepted: AcceptedRun{RunID: "run"}, NodeID: "job", SpecHash: source.SpecHash}
	grant, err := claims.evaluate(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Binding.RunID != "run" || grant.Binding.JobID != "job" {
		t.Fatalf("SDK source: %+v", grant.Binding)
	}
	if len(claims.nodes) != 0 || source.Attempt != 0 {
		t.Fatal("evaluation acquired a work award")
	}
	snapshot, err := registry.ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.NodeHost, Digest: sha256.Sum256([]byte(grant.Binding.Routes.Bearer))}, capability.RequestEvidence{})
	if err != nil || len(snapshot.Authorities) != 2 || snapshot.Authorities[0].Principal.Capability.Parent.ID != "owner" || snapshot.Authorities[0].Principal.Capability.Binding.Kind != capability.BindingTrigger {
		t.Fatalf("owner ancestry: %+v %v", snapshot, err)
	}
	if err := grant.Revoke(); err != nil {
		t.Fatal(err)
	}
	input.Generation++
	if _, err := claims.evaluate(t.Context(), input); err == nil {
		t.Fatal("stale source delegated")
	}
}
