package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestCloudPodOIDCKindIsRunner(t *testing.T) {
	st, ctx := openTestStore(t)
	tx, err := st.beginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	pod := capability.Capability{Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch}}
	facts, err := claimantFactsTx(ctx, tx, []authorityNode{{principal: capability.Principal{Capability: pod}}}, runspec.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Cloud || !facts.FreshHost || facts.OIDCKind != "runner" {
		t.Fatalf("Cloud pod facts: cloud=%v fresh=%v runner_kind=%q", facts.Cloud, facts.FreshHost, facts.OIDCKind)
	}
}

func TestClaimantReadsSealedHostFacts(t *testing.T) {
	st, ctx := openTestStore(t)
	tx, err := st.beginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, kind := range []capability.HostKind{capability.HostCloud, capability.HostSelfHosted, capability.HostPersonal, capability.HostHere} {
		host := &capability.HostFacts{Kind: kind, Fresh: kind == capability.HostCloud, RunnerKind: "runner", CacheScope: "sealed-cache", MachineID: "sealed-machine", OwnerAccountID: "sealed-owner"}
		// safety: the absent enrollment catches consumers that recompute sealed host facts.
		leaf := capability.Capability{Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "changed-machine"}, Host: host, Unit: capability.UnitNode}
		facts, err := claimantFactsTx(ctx, tx, []authorityNode{{principal: capability.Principal{Capability: leaf}}}, runspec.Request{})
		if err != nil {
			t.Fatal(err)
		}
		if facts.HostKind != kind || facts.FreshHost != host.Fresh || facts.Host != host.MachineID || facts.OwnerAccount != host.OwnerAccountID {
			t.Fatalf("sealed host changed: %+v", facts)
		}
	}
}

func TestOwnerMachineReadsTheSealedPersonalHereHost(t *testing.T) {
	tenant, auth := sourceReleaseFixture(t)
	fixtureRunWriter(t, tenant.s, "run", runspec.PrincipalWriter("account", "owner"))
	for _, kind := range []capability.HostKind{capability.HostPersonal, capability.HostHere} {
		setSourceHost(t, tenant, auth, &capability.HostFacts{Kind: kind, RunnerKind: tierPersonal, OwnerAccountID: "owner", MachineID: "runner"})
		owner, err := tenant.OwnerMachineClaimant(t.Context(), auth.Principal, "run")
		if err != nil || !owner {
			t.Fatalf("sealed %s host lost its owner: owner=%v error=%v", kind, owner, err)
		}
	}
}

func TestUnknownClaimantsCannotExecuteProvenRepositoryRuns(t *testing.T) {
	st, ctx := authorityStore(t), t.Context()
	machine := authorityRoot("unknown", "a")
	machine.Grants = nil
	authorityPersist(t, st, machine)
	tx, err := st.beginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, kind := range []capability.SubjectKind{capability.SubjectAccount, capability.SubjectService, capability.SubjectOperator, capability.SubjectMachine, "unknown"} {
		leaf := machine
		leaf.Subject.Kind = kind
		facts, err := claimantFactsTx(ctx, tx, []authorityNode{{principal: capability.Principal{Capability: leaf}}}, runspec.Request{})
		if err != nil {
			t.Fatal(err)
		}
		if facts.HostKind != capability.HostPersonal || facts.HostKind.AllowsProvenRepository() {
			t.Fatalf("unknown %s claimant received host kind %s", kind, facts.HostKind)
		}
	}
}
