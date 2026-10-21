package store

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func assertAwardBox(t *testing.T, s *Store, ref capability.AuthorityRef, run, host string) {
	t.Helper()
	p, err := s.AsOperator().ResolveAuthority(t.Context(), ref)
	if err != nil || p.Capability.ExecutionHost != host {
		t.Fatalf("persisted award host: %q %v; want %q", p.Capability.ExecutionHost, err, host)
	}
	err = s.inTx(t.Context(), func(tx *storeTx) error {
		key, err := capacityKeyTx(t.Context(), tx, ref, run, "job", "box", "serial")
		if err != nil || key != runspec.ConcurrencyKey("job", "box", host, "serial") {
			t.Fatalf("award box: %q %v; want host %q", key, err, host)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAwardBoxIdentityForActions(t *testing.T) {
	tenant, _, _ := actionsEnrollment(t)
	s, demand := unitClaimFixtureStore(t, tenant.s)
	spec := actionsSpec(t, s, tenant.team, "1000")
	spec.Binding.Push.SHA = fixtureOtherSHA
	issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	p, err := runspec.ActionsProvenance(42, spec.Binding.Push.Ref, spec.Binding.Push.SHA, "ci")
	if err != nil {
		t.Fatal(err)
	}
	body := sealWith(t, runspec.SealedRequest{Request: runspec.Request{Git: runspec.Git{Repository: runspec.Repository{Key: "gh:42"}}}}, p)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,repo_key='gh:42' WHERE id='run'`, body)
	demand.CapabilityPrefix = issued.Capability.Prefix
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}
	assertAwardBox(t, s, ref, "run", spec.Subject.ID)
	persisted, err := s.AsOperator().ResolveAuthority(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	delegated, err := s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: ref, Audience: capability.Controller, Grants: []capability.Grant{capability.NodeResult}, Binding: persisted.Capability.Binding, ExpiresAt: persisted.Capability.ExpiresAt, Repositories: persisted.Capability.Repositories})
	if err != nil {
		t.Fatal(err)
	}
	assertAwardBox(t, s, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: delegated.Capability.Prefix}, "run", spec.Subject.ID)
}

func TestAwardBoxIdentityForAccountHere(t *testing.T) {
	tenant, request, now := intakeFixture(t)
	owner := authorityMember(t, tenant.s, "account", RoleOwner)
	machine, err := tenant.s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
		Kind: capability.ProvisionMachine, Personal: true, Authority: &owner.Authority,
		RegistrationID: "runner:a:laptop", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "laptop"},
		Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone},
		Repositories: []string{"github.com/acme/*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request.Authority = owner.Authority
	request.IdempotencyKey = "here"
	request.OriginMachine = capability.HereProof(machine.Raw, request.IdempotencyKey, *now)
	request.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
	admitted, err := tenant.AdmitRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertAwardBox(t, tenant.s, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: admitted.HereCapabilityPrefix}, admitted.RunID, "laptop")
	claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: admitted.HereCapabilityPrefix, Target: &UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(admitted.RunID)}, Capacity: match.Resources{Cores: 1}, Available: match.Resources{Cores: 1}})
	if err != nil {
		t.Fatal(err)
	}
	assertAwardBox(t, tenant.s, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, admitted.RunID, "laptop")
}

func TestAwardBoxIdentityForMachineAndLocalPeer(t *testing.T) {
	for _, kind := range []capability.SubjectKind{capability.SubjectMachine, capability.SubjectLocal} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			root := authorityRoot("host", "a")
			root.Subject.Kind = kind
			authorityPersist(t, s, root)
			demand.CapabilityPrefix = root.Prefix
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			assertAwardBox(t, s, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, "run", "host")
		})
	}
}

func TestLauncherSealsBoxBeforeExchangeAndRefusesAnotherNode(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) { testLauncherSealedBox(t, kind) })
	}
}

func testLauncherSealedBox(t *testing.T, kind capability.UnitKind) {
	t.Helper()
	s, demand, claim, _ := launcherSnapshotFixture(t, kind)
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
	unitID, err := encodeAwardUnit(*demand.Target)
	if err != nil {
		t.Fatal(err)
	}
	observation := controllerwire.LauncherObservation{
		Award: capability.LeaseRef{Kind: claim.Kind, ID: unitID, Generation: int64(claim.Award.Generation), Holder: claim.Award.Holder},
	}
	activation := controllerwire.LauncherActivation{Award: observation.Award, NodeUID: "node-uid", ReservationID: "reservation"}
	for _, reservation := range []bool{false, true} {
		observed := observation
		observed.Reservation = reservation
		if _, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), ref, []controllerwire.LauncherObservation{observed}, nil, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := capacityKeyTx(t.Context(), tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, "run", "job", "box", "serial")
		if !errors.Is(err, errLauncherHostPending) {
			t.Fatalf("unplaced launcher box admission must wait: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), ref, []controllerwire.LauncherObservation{observation}, nil, nil, nil, []controllerwire.LauncherActivation{activation}); err != nil {
			t.Fatal(err)
		}
	}
	assertAwardBox(t, s, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, "run", "node:node-uid")
	exchanged, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	assertAwardBox(t, s, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: exchanged.Capability.Prefix}, "run", "node:node-uid")
	activation.NodeUID = "other-node"
	if _, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), ref, []controllerwire.LauncherObservation{observation}, nil, nil, nil, []controllerwire.LauncherActivation{activation}); err == nil {
		t.Fatal("changed the node of an exchanged award")
	}
	assertAwardBox(t, s, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: exchanged.Capability.Prefix}, "run", "node:node-uid")
}

func TestLauncherExpiredUnplacedAwardCanReleaseReservation(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			for name, exchange := range map[string]bool{"bootstrap": false, "exchanged": true} {
				t.Run(name, func(t *testing.T) {
					s, demand, claim, now := launcherSnapshotFixture(t, kind)
					prefix := claim.Award.CapabilityPrefix
					if exchange {
						issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: prefix})
						if err != nil {
							t.Fatal(err)
						}
						prefix = issued.Capability.Prefix
					}
					unitID, err := encodeAwardUnit(*demand.Target)
					if err != nil {
						t.Fatal(err)
					}
					observation := controllerwire.LauncherObservation{
						Award:       capability.LeaseRef{Kind: kind, ID: unitID, Generation: int64(claim.Award.Generation), Holder: claim.Award.Holder},
						Reservation: true,
					}
					*now = claim.Award.LeaseExpiresAt
					awards, prepared, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}, []controllerwire.LauncherObservation{observation}, []capability.LeaseRef{observation.Award}, []capability.LeaseRef{observation.Award}, nil, nil)
					if err != nil || len(awards) != 1 || !awards[0].Gone || awards[0].Fence != observation.Award || len(prepared) != 0 {
						t.Fatalf("expired reservation (exchange=%t): awards=%+v prepared=%+v: %v", exchange, awards, prepared, err)
					}
					if err := s.inTx(t.Context(), func(tx *storeTx) error {
						c, err := loadCapabilityTx(t.Context(), tx, prefix)
						if err == nil && c.ExecutionHost != "" {
							t.Fatalf("expired award acquired host %q", c.ExecutionHost)
						}
						return err
					}); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}
