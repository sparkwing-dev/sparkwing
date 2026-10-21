package nodehost

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type processParent struct {
	mu       sync.Mutex
	snapshot capability.AuthenticationSnapshot
	err      error
	lookups  []capability.CredentialLookup
	evidence []capability.RequestEvidence
	read     func()
}

type processBindingFunc func(context.Context, capability.Principal, capability.BindingRule, capability.Target) error

func (f processBindingFunc) CheckBinding(ctx context.Context, principal capability.Principal, rule capability.BindingRule, target capability.Target) error {
	return f(ctx, principal, rule, target)
}

func (p *processParent) ReadAuthentication(_ context.Context, lookup capability.CredentialLookup, evidence capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	p.mu.Lock()
	p.lookups = append(p.lookups, lookup)
	p.evidence = append(p.evidence, evidence)
	snapshot, err, read := p.snapshot, p.err, p.read
	if err == nil && (len(snapshot.Authorities) == 0 || lookup.Digest != snapshot.Authorities[0].Principal.Capability.Digest || lookup.Class != capability.CredentialBearer) {
		err = processUnauthenticated()
	}
	p.mu.Unlock()
	if read != nil {
		read()
	}
	return snapshot, err
}

func processAuthorityFixture(t *testing.T) (*ProcessAuthorities, *processParent, capability.CredentialLookup, capability.DelegateSpec, *ProcessServices) {
	t.Helper()
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)
	binding := capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Generation: 4, Attempt: 2, Holder: "holder"}
	principal := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "parent"}, Capability: capability.Capability{
		Unit: capability.UnitNode, Prefix: "parent", Digest: sha256.Sum256([]byte("parent-token")), Audience: capability.Controller, Team: "team", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "machine"},
		Binding: binding, Grants: []capability.Grant{capability.NodeReport, capability.SecretsRelease}, ExpiresAt: &expires, ReservedPools: []string{"pool"}, Repositories: []string{"repo"},
	}}
	parent := &processParent{snapshot: capability.AuthenticationSnapshot{Now: now, Authorities: []capability.AuthorityState{{Principal: principal}}, Binding: capability.BindingState{Current: binding, LeaseExpiresAt: expires, StartPermitted: true}}}
	registry, err := NewProcessAuthorities(ProcessAuthorityDependencies{
		Parent: parent,
		Bindings: processBindingFunc(func(context.Context, capability.Principal, capability.BindingRule, capability.Target) error {
			return nil
		}),
		ValidateAuthority: func(chain []capability.AuthorityState, now time.Time) (capability.Principal, error) {
			for _, state := range chain {
				if !capability.CredentialAlive(state.Principal.Capability, now) {
					return capability.Principal{}, executorError(apierr.Unauthenticated, "parent ended")
				}
			}
			return chain[0].Principal, nil
		},
		ValidateDelegation: func(parent capability.Principal, child capability.DelegateSpec, _ time.Time) error {
			if parent.Authority != child.Parent || !reflect.DeepEqual(parent.Capability.Binding, child.Binding) {
				return executorError(apierr.Forbidden, "changed delegation")
			}
			for _, grant := range child.Grants {
				if !slices.Contains(parent.Capability.Grants, grant) {
					return executorError(apierr.Forbidden, "widened delegation")
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: principal.Capability.Digest}
	spec := capability.DelegateSpec{Parent: principal.Authority, Audience: capability.NodeHost, Binding: binding, Grants: []capability.Grant{capability.NodeReport}, ExpiresAt: &expires, Repositories: []string{"repo"}}
	service := &ProcessServices{RunID: "run", Job: nodeproto.JobSpec{ID: "job", Steps: []nodeproto.StepSpec{{ID: "step"}}}, Masks: NewMaskRegistry(secrets.NewMasker())}
	return registry, parent, lookup, spec, service
}

func processLookup(issued capability.Issued) capability.CredentialLookup {
	return capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.NodeHost, Digest: sha256.Sum256([]byte(issued.Raw))}
}

func processAuthorized(issued capability.Issued) capability.Authorized {
	principal := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, Capability: issued.Capability}
	return capability.Authorized{Principal: principal, Binding: issued.Capability.Binding}
}

func TestProcessAuthorityRetainsFreshParentAndExactFence(t *testing.T) {
	registry, parent, lookup, spec, service := processAuthorityFixture(t)
	issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = revoke() })
	if !strings.HasPrefix(issued.Raw, capability.NodeHostPrefix) || issued.Capability.Digest != sha256.Sum256([]byte(issued.Raw)) || issued.Capability.Parent == nil || *issued.Capability.Parent != spec.Parent || !reflect.DeepEqual(issued.Capability.Binding, spec.Binding) {
		t.Fatalf("invalid delegated identity: %+v", issued.Capability)
	}
	if service.Masks.mask(issued.Raw) != "***" {
		t.Fatal("process bearer was delivered before mask registration")
	}
	evidence := capability.RequestEvidence{Method: "POST", Pattern: "/node/v1/summary", ResultDigest: "exact-body"}
	first, err := registry.ReadAuthentication(t.Context(), processLookup(issued), evidence)
	if err != nil || len(first.Authorities) != 2 || !first.Binding.ProcessAlive || first.Authorities[0].Principal.Capability.Audience != capability.NodeHost {
		t.Fatalf("snapshot=%+v error=%v", first, err)
	}
	parent.mu.Lock()
	parent.snapshot.Now = parent.snapshot.Now.Add(time.Minute)
	parent.snapshot.Binding.RunCancelled = true
	parent.snapshot.Binding.Current.Attempt++
	parent.snapshot.Authorities[0].Principal.Capability.RevokedAt = &parent.snapshot.Now
	parent.mu.Unlock()
	second, err := registry.ReadAuthentication(t.Context(), processLookup(issued), evidence)
	if err != nil || !second.Now.After(first.Now) || !second.Binding.RunCancelled || capability.CredentialAlive(second.Authorities[1].Principal.Capability, second.Now) || second.Binding.Current.Attempt != spec.Binding.Attempt+1 || second.Authorities[0].Principal.Capability.Binding.Attempt != spec.Binding.Attempt {
		t.Fatalf("stale ancestry or liveness: %+v %v", second, err)
	}
	if parent.lookups[len(parent.lookups)-1] != lookup || !reflect.DeepEqual(parent.evidence[len(parent.evidence)-1], evidence) {
		t.Fatal("parent lookup lost digest or exact route evidence")
	}
}

func TestProcessAuthorityRevocationRefusesReadersAndCancelsServices(t *testing.T) {
	registry, parent, lookup, spec, service := processAuthorityFixture(t)
	issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service)
	if err != nil {
		t.Fatal(err)
	}
	auth := processAuthorized(issued)
	live, err := registry.Resolve(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := revoke(); err != nil {
		t.Fatal(err)
	}
	if context.Cause(live.lifetime) == nil {
		t.Fatal("revocation left the service lifetime open")
	}
	for range 2 {
		if _, err := registry.ReadAuthentication(t.Context(), processLookup(issued), capability.RequestEvidence{}); err == nil {
			t.Fatal("revoked process remained readable")
		}
		if _, err := registry.Resolve(t.Context(), auth); err == nil {
			t.Fatal("revoked process reached its services")
		}
		if err := revoke(); err != nil {
			t.Fatal(err)
		}
	}
	if len(parent.lookups) != 1 {
		t.Fatal("unknown process disclosed parent authority")
	}
}

func TestProcessAuthorityRevocationWinsAnInflightParentRead(t *testing.T) {
	registry, parent, lookup, spec, service := processAuthorityFixture(t)
	issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service)
	if err != nil {
		t.Fatal(err)
	}
	parent.read = func() {
		if err := revoke(); err != nil {
			t.Error(err)
		}
	}
	if _, err := registry.ReadAuthentication(t.Context(), processLookup(issued), capability.RequestEvidence{}); err == nil {
		t.Fatal("parent lookup resurrected a revoked process")
	}
}

func TestProcessAuthorityRejectsUnknownCredentialsBeforeParentRead(t *testing.T) {
	registry, parent, lookup, spec, service := processAuthorityFixture(t)
	issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = revoke() })
	for _, raw := range []string{"", "swk_controller-parent", strings.Repeat("A", len(issued.Raw)), issued.Raw[:len(issued.Raw)-1], issued.Raw + "x"} {
		bad := processLookup(issued)
		bad.Digest = sha256.Sum256([]byte(raw))
		_, err := registry.ReadAuthentication(t.Context(), bad, capability.RequestEvidence{})
		var canonical *apierr.Error
		if !errors.As(err, &canonical) || canonical.Code != apierr.Unauthenticated {
			t.Fatalf("unknown credential result=%v", err)
		}
	}
	bad := processLookup(issued)
	bad.Class = capability.CredentialSession
	if _, err := registry.ReadAuthentication(t.Context(), bad, capability.RequestEvidence{}); err == nil {
		t.Fatal("process lookup accepted another credential class")
	}
	if len(parent.lookups) != 1 {
		t.Fatal("unknown process credential reached parent authority")
	}
}

func TestProcessAuthorityPreservesAudienceForSharedAuthorization(t *testing.T) {
	registry, _, lookup, spec, service := processAuthorityFixture(t)
	issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = revoke() })
	wrongAudience := processLookup(issued)
	wrongAudience.Audience = capability.Controller
	for _, test := range []struct {
		lookup capability.CredentialLookup
		want   capability.Audience
	}{
		{wrongAudience, capability.NodeHost},
		{lookup, capability.Controller},
	} {
		snapshot, err := registry.ReadAuthentication(t.Context(), test.lookup, capability.RequestEvidence{})
		if err != nil || snapshot.Authorities[0].Principal.Capability.Audience != test.want {
			t.Fatal("reader changed identity or preempted shared audience authorization")
		}
	}
}

func TestProcessAuthorityCloseRevokesAllAndRefusesNewDelegation(t *testing.T) {
	registry, _, lookup, spec, service := processAuthorityFixture(t)
	a, _, err := registry.Delegate(t.Context(), lookup, spec, service)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := registry.Delegate(t.Context(), lookup, spec, service)
	if err != nil {
		t.Fatal(err)
	}
	if a.Raw == b.Raw || a.Capability.Digest == b.Capability.Digest || a.Capability.Prefix == b.Capability.Prefix {
		t.Fatal("two admissions shared process authority")
	}
	for range 2 {
		if err := registry.Close(); err != nil {
			t.Fatal(err)
		}
		for _, issued := range []capability.Issued{a, b} {
			if _, err := registry.Resolve(t.Context(), processAuthorized(issued)); err == nil {
				t.Fatal("closed registry retained process services")
			}
			if _, err := registry.ReadAuthentication(t.Context(), processLookup(issued), capability.RequestEvidence{}); err == nil {
				t.Fatal("closed registry retained process authentication")
			}
		}
		if issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service); err == nil || issued.Raw != "" || revoke != nil {
			t.Fatal("closed registry admitted a new process")
		}
	}
}

func TestProcessAuthorityCloseWinsInflightDelegation(t *testing.T) {
	registry, parent, lookup, spec, service := processAuthorityFixture(t)
	parent.read = func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	}
	if issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service); err == nil || issued.Raw != "" || revoke != nil {
		t.Fatal("in-flight admission reopened a closed registry")
	}
}

func TestProcessAuthorityRefusesInvalidDelegationWithoutIssuing(t *testing.T) {
	for _, mode := range []string{"grant", "parent", "binding", "audience", "service", "masks", "ended", "read", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			registry, parent, lookup, spec, service := processAuthorityFixture(t)
			ctx := t.Context()
			switch mode {
			case "grant":
				spec.Grants = append(spec.Grants, capability.TeamAdmin)
			case "parent":
				spec.Parent.ID = "other"
			case "binding":
				spec.Binding.Attempt++
			case "audience":
				spec.Audience = capability.Controller
			case "service":
				service.Job.ID = "other"
			case "masks":
				service.Masks = nil
			case "ended":
				parent.snapshot.Authorities[0].Principal.Capability.RevokedAt = &parent.snapshot.Now
			case "read":
				parent.err = errors.New("parent read refused")
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			issued, revoke, err := registry.Delegate(ctx, lookup, spec, service)
			if err == nil || issued.Raw != "" || revoke != nil {
				t.Fatal("invalid authority issued a process bearer")
			}
		})
	}
}

func TestProcessAuthoritySnapshotsCannotMutateStoredAuthority(t *testing.T) {
	registry, _, lookup, spec, service := processAuthorityFixture(t)
	issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = revoke() })
	spec.Grants[0] = capability.TeamAdmin
	issued.Capability.Repositories[0] = "other"
	service.Job.Steps[0].ID = "other"
	first, err := registry.ReadAuthentication(t.Context(), processLookup(issued), capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	first.Authorities[0].Principal.Capability.Grants[0] = capability.TeamAdmin
	second, err := registry.ReadAuthentication(t.Context(), processLookup(issued), capability.RequestEvidence{})
	if err != nil || second.Authorities[0].Principal.Capability.Grants[0] != capability.NodeReport || second.Authorities[0].Principal.Capability.Repositories[0] != "repo" {
		t.Fatalf("stored authority was mutable: %+v %v", second, err)
	}
	principal := second.Authorities[0].Principal
	live, err := registry.Resolve(t.Context(), capability.Authorized{Principal: principal, Binding: principal.Capability.Binding})
	if err != nil || live.Job.Steps[0].ID != "step" {
		t.Fatal("caller mutated accepted service declarations")
	}
}

func TestProcessAuthorityRevokesInflightServiceBeforeAcknowledgement(t *testing.T) {
	registry, _, lookup, spec, service := processAuthorityFixture(t)
	entered := make(chan struct{})
	service.Summary = func(ctx context.Context, _ capability.Authorized, _ nodeproto.SummaryRequest) error {
		close(entered)
		<-ctx.Done()
		return nil
	}
	issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = revoke() })
	routes := &serviceRegistrar{}
	if err := RegisterProcessServices(routes, registry.Resolve); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		out, err := callService(t, routes, processAuthorized(issued), "/node/v1/summary", `{"markdown":"text"}`)
		if out.Body.Len() != 0 {
			finished <- errors.New("revoked service acknowledged success")
			return
		}
		finished <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("service did not begin")
	}
	if err := revoke(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("revoked service result=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("revoked service did not join")
	}
}

func TestProcessAuthorityBindingChecksKeepLocalAndPersistedOwnership(t *testing.T) {
	registry, _, lookup, spec, service := processAuthorityFixture(t)
	service.Secrets = []string{"TOKEN"}
	service.Cleanups = &ProcessCleanups{}
	id, err := service.Cleanups.Register([]string{"tool"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	sentinel := errors.New("persisted relation refused")
	registry.deps.Bindings = processBindingFunc(func(_ context.Context, process capability.Principal, _ capability.BindingRule, target capability.Target) error {
		calls++
		if process.Authority != spec.Parent || process.Capability.Audience != capability.Controller || !reflect.DeepEqual(process.Capability.Binding, spec.Binding) || target.RunID != "run" || target.Team != "team" {
			t.Fatal("relation checker lost the persisted parent or process target")
		}
		return sentinel
	})
	issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = revoke() })
	principal := processAuthorized(issued).Principal
	for _, test := range []struct {
		rule   capability.BindingRule
		target capability.Target
	}{
		{capability.BindOwnNode, capability.Target{NodeID: "other"}},
		{capability.BindOwnNode, capability.Target{RunID: "other"}},
		{capability.BindOwnNode, capability.Target{Team: "other"}},
		{capability.BindDeclaredSecret, capability.Target{SecretName: "UNDECLARED"}},
		{capability.BindBodyNamesRun, capability.Target{BodyRunID: "other"}},
		{capability.BindOwnCleanup, capability.Target{CleanupID: "other"}},
		{capability.BindNone, capability.Target{}},
	} {
		if err := registry.CheckBinding(t.Context(), principal, test.rule, test.target); err == nil {
			t.Fatal("foreign process relation accepted")
		}
	}
	if calls != 0 {
		t.Fatal("local refusal reached persistence")
	}
	for _, rule := range []capability.BindingRule{capability.BindOwnNode, capability.BindReadSet, capability.BindDeclaredSecret, capability.BindBodyNamesRun, capability.BindChildOfBoundNode} {
		if err := registry.CheckBinding(t.Context(), principal, rule, capability.Target{SecretName: "TOKEN"}); !errors.Is(err, sentinel) {
			t.Fatalf("%s lost the persisted refusal: %v", rule, err)
		}
	}
	if calls != 5 {
		t.Fatal("missing persisted relation check")
	}
	if err := registry.CheckBinding(t.Context(), principal, capability.BindOwnCleanup, capability.Target{CleanupID: id}); err != nil {
		t.Fatal(err)
	}
	if err := service.Cleanups.Release(id); err != nil {
		t.Fatal(err)
	}
	if err := registry.CheckBinding(t.Context(), principal, capability.BindOwnCleanup, capability.Target{CleanupID: id}); err == nil {
		t.Fatal("released cleanup remained owned")
	}
}

func FuzzProcessAuthorityFenceAndRevocation(f *testing.F) {
	f.Add("run", "job", int64(4), int64(2), "holder", false)
	f.Add("other", "job", int64(4), int64(2), "holder", false)
	f.Add("run", "job", int64(4), int64(2), "holder", true)
	f.Fuzz(func(t *testing.T, run, job string, generation, attempt int64, holder string, revoked bool) {
		registry, _, lookup, spec, service := processAuthorityFixture(t)
		issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = revoke() })
		auth := processAuthorized(issued)
		auth.Binding.RunID, auth.Binding.NodeID, auth.Binding.Generation, auth.Binding.Attempt, auth.Binding.Holder = run, job, generation, attempt, holder
		if revoked {
			if err := revoke(); err != nil {
				t.Fatal(err)
			}
		}
		_, err = registry.Resolve(t.Context(), auth)
		valid := !revoked && reflect.DeepEqual(auth.Binding, spec.Binding)
		if (err == nil) != valid {
			t.Fatalf("changed or revoked fence accepted=%v, expected=%v", err == nil, valid)
		}
		identityLookup := processLookup(issued)
		identityLookup.Audience = capability.Audience(run)
		snapshot, err := registry.ReadAuthentication(t.Context(), identityLookup, capability.RequestEvidence{})
		if revoked {
			if err == nil {
				t.Fatal("revoked process returned authentication facts")
			}
		} else if err != nil || snapshot.Authorities[0].Principal.Capability.Audience != capability.NodeHost {
			t.Fatal("lookup audience reclassified process identity")
		}
	})
}

func TestProcessDelegationRequiresCommittedJobAdmission(t *testing.T) {
	registry, parent, lookup, spec, services := processAuthorityFixture(t)
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	})
	parent.snapshot.Binding.StartPermitted = false
	if issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, services); err == nil || issued.Raw != "" || revoke != nil {
		t.Fatalf("job process bypassed admission: issued=%+v revoke=%v error=%v", issued, revoke != nil, err)
	}
	parent.snapshot.Binding.StartPermitted = true
	issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, services)
	if err != nil || issued.Raw == "" || revoke == nil {
		t.Fatalf("admitted process failed: issued=%+v error=%v", issued, err)
	}
	if err := revoke(); err != nil {
		t.Fatal(err)
	}
}
