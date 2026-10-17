package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func burstFixture(t *testing.T, policy runspec.BurstPlacement) (*Store, UnitDemand) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	body, err := json.Marshal(runspec.SealedRequest{Request: runspec.Request{Burst: &policy, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}}})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,created_principal='owner',created_subject_kind='account'`, body)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=?`, s.now().UnixNano())
	return s, demand
}

func burstPersonalClaimant(t *testing.T, s *Store, authority capability.AuthorityRef, name string) capability.Capability {
	t.Helper()
	issued, err := s.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &authority, RegistrationID: "runner:a:" + name, Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: name}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Capability
}

func TestBurstPersonalMachineOwnershipProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(816, 192))
	for i := range 32 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, demand := burstFixture(t, runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "machines"}, {Target: "cloud"}}})
			owner := authorityMember(t, s, "owner", RoleOwner)
			other := authorityMember(t, s, fmt.Sprintf("other-%x", random.Uint64()), RoleEditor)
			authority := owner.Authority
			allowed := i%2 == 0
			if !allowed {
				authority = other.Authority
			}
			issued, err := s.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &authority, RegistrationID: "runner:a:mine", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "mine"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
			if err != nil {
				t.Fatal(err)
			}
			if i%4 == 0 {
				rewriteSchemaExec(t, s.db, `UPDATE runs SET created_subject_kind='machine'`)
				allowed = false
			}
			demand.CapabilityPrefix = issued.Capability.Prefix
			demand.Labels = []string{"class=coordinator", "name=owner", "pool=trusted"}
			if i%3 == 0 {
				demand.Target = nil
				demand.Kinds = []capability.UnitKind{capability.UnitNode}
			}
			claim, err := s.ClaimUnit(t.Context(), demand)
			if allowed && (err != nil || claim.NodeID != "job") {
				t.Fatalf("owner claim: %+v %v", claim, err)
			}
			if !allowed && err == nil {
				t.Fatal("personal machine claimed another user's run")
			}
		})
	}
}

func TestBurstSealedPreferencesOverrideControllerPlacement(t *testing.T) {
	s, demand := burstFixture(t, runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "name=preferred", Hold: time.Minute}, {Target: "machines"}, {Target: "cloud"}}})
	owner := authorityMember(t, s, "owner", RoleOwner)
	root := burstPersonalClaimant(t, s, owner.Authority, "preferred")
	fallback := burstPersonalClaimant(t, s, owner.Authority, "fallback")
	demand.CapabilityPrefix = fallback.Prefix
	if err := s.RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Capacity: demand.Capacity, Available: demand.Available}); err != nil {
		t.Fatal(err)
	}
	ctx := WithClaimPlacement(t.Context(), ClaimPlacement{Preferences: []PlacementPreference{{Prefers: []string{"name=runner"}}}, Liveness: time.Minute})
	_, err := s.ClaimUnit(ctx, demand)
	var denied *apierr.Error
	if !errors.As(err, &denied) || denied.Code != apierr.LockHeld {
		t.Fatalf("sealed earlier machine not held: %v", err)
	}
	now := s.now().Add(time.Minute)
	s.clock = func() time.Time { return now }
	if _, err := s.ClaimUnit(ctx, demand); err != nil {
		t.Fatal(err)
	}
}

func TestPersonalMachineCannotClaimNonOwnerNonBurstRun(t *testing.T) {
	s, demand := unitClaimFixture(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	issued, err := s.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &owner.Authority, RegistrationID: "runner:a:mine", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "mine"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	demand.CapabilityPrefix = issued.Capability.Prefix
	if _, err := s.ClaimUnit(t.Context(), demand); err == nil {
		t.Fatal("personal root escaped owner scope outside burst")
	}
}

func TestOverflowLimitIsPerUserAndDefaultsUncapped(t *testing.T) {
	s, _ := burstFixture(t, runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "cloud"}}})
	tenant := &Tenant{s: s, team: "a"}
	owner := authorityMember(t, s, "owner", RoleOwner)
	other := authorityMember(t, s, "other", RoleEditor)
	initial, err := tenant.ReadOverflowControl(t.Context(), owner.Authority)
	if err != nil || initial.MonthlyMinutes != nil || initial.UsedSeconds != 0 {
		t.Fatalf("default: %+v %v", initial, err)
	}
	zero := int64(0)
	if err := tenant.SetOverflowLimit(t.Context(), owner.Authority, &zero); err != nil {
		t.Fatal(err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error { return checkOverflowTx(t.Context(), tx, "a", "run") }); err == nil {
		t.Fatal("zero limit admitted Cloud")
	}
	untouched, err := tenant.ReadOverflowControl(t.Context(), other.Authority)
	if err != nil || untouched.MonthlyMinutes != nil {
		t.Fatalf("another user's setting changed: %+v %v", untouched, err)
	}
	if err := tenant.SetOverflowLimit(t.Context(), owner.Authority, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error { return checkOverflowTx(t.Context(), tx, "a", "run") }); err != nil {
		t.Fatal(err)
	}
}

func TestOverflowCannotBeReadOrChangedWithMachineAuthorityProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(112, 116))
	for i := range 24 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, _ := unitClaimFixture(t)
			tenant := &Tenant{s: s, team: "a"}
			root := authorityRoot(fmt.Sprintf("machine-%x", random.Uint64()), "a")
			root.Grants = []capability.Grant{capability.RunsRead, capability.RunsWrite, capability.TeamAdmin}
			authorityPersist(t, s, root)
			authority := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
			value := random.Int64N(1000)
			if err := tenant.SetOverflowLimit(t.Context(), authority, &value); err == nil {
				t.Fatal("machine wrote account limit")
			}
			if _, err := tenant.ReadOverflowControl(t.Context(), authority); err == nil {
				t.Fatal("machine read account limit")
			}
		})
	}
}

func burstHereOwnerFixture(t *testing.T) (*Store, UnitDemand, capability.Principal) {
	t.Helper()
	s, demand := burstFixture(t, runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here", Hold: time.Minute}, {Target: "machines"}, {Target: "cloud"}}})
	rewriteSchemaExec(t, s.db, `UPDATE triggers SET created_at=?`, s.now().UnixNano())
	root := authorityRoot("local-owner", "a")
	root.Subject.Kind = capability.SubjectLocal
	root.Grants = []capability.Grant{capability.TriggersClaim, capability.NodesClaim, capability.RunsWrite}
	authorityPersist(t, s, root)
	var here capability.Issued
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		var err error
		here, err = s.Issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Award, Award: &capability.AwardSpec{Claimant: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Unit: capability.UnitTrigger, UnitID: "run", HereRunID: "run", Purpose: capability.PurposeHere, Delivery: capability.DeliveryDirect}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	demand.CapabilityPrefix = here.Capability.Prefix
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	if err := s.RecordRunnerPoll(t.Context(), demand); err != nil {
		t.Fatal(err)
	}
	owner, err := s.ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: owner.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	return s, demand, principal
}

func TestBurstHereOwnerCapacityComesFromItsFencedObservation(t *testing.T) {
	s, demand, principal := burstHereOwnerFixture(t)
	fallback := demand
	owner := authorityMember(t, s, "owner", RoleOwner)
	fallback.CapabilityPrefix = burstPersonalClaimant(t, s, owner.Authority, "fallback").Prefix
	fallback.Target = &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}
	if _, err := s.ClaimUnit(t.Context(), fallback); err == nil {
		t.Fatal("live here owner did not hold fallback")
	}
	node := NodeRef{RunID: "run", NodeID: "job"}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		eligible, err := ownerExecutionEligibleTx(t.Context(), tx, principal, node, nil)
		if err != nil {
			return err
		}
		if !eligible {
			t.Fatal("here owner cannot execute")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	demand.CapabilityPrefix = principal.Capability.Prefix
	demand.Available.Cores = 0
	if err := s.RecordHereAvailability(t.Context(), principal.Authority, demand.Available); err != nil {
		t.Fatal(err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		reserved := demand.Capacity
		eligible, err := ownerExecutionEligibleTx(t.Context(), tx, principal, node, &reserved)
		if err != nil || !eligible {
			t.Fatalf("held reservation was double-counted as occupied: eligible=%v error=%v", eligible, err)
		}
		eligible, err = ownerExecutionEligibleTx(t.Context(), tx, principal, node, nil)
		if err != nil || eligible {
			t.Fatalf("reservation leaked to other jobs: eligible=%v error=%v", eligible, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimUnit(t.Context(), fallback); err != nil {
		t.Fatalf("saturated here owner blocked fallback: %v", err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		eligible, err := ownerExecutionEligibleTx(t.Context(), tx, principal, node, nil)
		if err != nil {
			return err
		}
		if eligible {
			t.Fatal("saturated here owner can execute")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBurstMachinesPreferenceIncludesEveryOwnersRegisteredMachine(t *testing.T) {
	s, demand := burstFixture(t, runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "machines", Hold: time.Minute}, {Target: "cloud"}}})
	owner := authorityMember(t, s, "owner", RoleOwner)
	for _, name := range []string{"desk-a", "desk-b"} {
		issued, err := s.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &owner.Authority, RegistrationID: "runner:a:" + name, Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: name}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
		if err != nil {
			t.Fatal(err)
		}
		if name == "desk-b" {
			if err := s.RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Capacity: demand.Capacity, Available: demand.Available}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.ClaimUnit(t.Context(), demand); err == nil {
		t.Fatal("second registered machine did not hold fallback")
	}
}

func TestBurstOverflowRequestNeverRaisesStoredBudgetProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(112, 216))
	for i := range 32 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			requested := random.Int64N(3)
			stored := random.Int64N(3)
			s, _ := burstFixture(t, runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "cloud"}}, OverflowMinutes: &requested})
			owner := authorityMember(t, s, "owner", RoleOwner)
			tenant := &Tenant{s: s, team: "a"}
			if err := tenant.SetOverflowLimit(t.Context(), owner.Authority, &stored); err != nil {
				t.Fatal(err)
			}
			err := s.inTx(t.Context(), func(tx *storeTx) error { return checkOverflowTx(t.Context(), tx, "a", "run") })
			if min(stored, requested) == 0 && err == nil {
				t.Fatal("request bypassed lower user limit")
			}
			if min(stored, requested) > 0 && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOverflowUsageIncludesCloudWorkAcrossMonthBoundary(t *testing.T) {
	s, _ := burstFixture(t, runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "cloud"}}})
	owner := authorityMember(t, s, "owner", RoleOwner)
	now := time.Date(2026, time.November, 1, 0, 2, 0, 0, time.UTC)
	s.clock = func() time.Time { return now }
	start := now.Add(-4 * time.Minute)
	rewriteSchemaExec(t, s.db, `INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at,reserved_amount_micro,requester_account) VALUES ('a','award','cloud','run',1,'a',1,'cloud','at_start','node','job',?,0,'owner')`, start.UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO billable_intervals(team,id,run_id,generation,capability_prefix,reservation_id,payer_team,metered,executor_class,unit_kind,billing_unit,billing_start,started_at,charged_through) VALUES ('a','interval','run',1,'cloud','award','a',1,'cloud','node','job','at_start',?,?)`, start.UnixNano(), start.UnixNano())
	control, err := (&Tenant{s: s, team: "a"}).ReadOverflowControl(t.Context(), owner.Authority)
	if err != nil || control.UsedSeconds != 120 {
		t.Fatalf("usage across month: %+v %v", control, err)
	}
}

func FuzzBurstReservationKeepsCapacityAndClaimFences(f *testing.F) {
	for i := range uint8(9) {
		f.Add(i, uint16(1))
	}
	f.Fuzz(func(t *testing.T, attack uint8, amount uint16) {
		s, _, owner := burstHereOwnerFixture(t)
		if err := s.RecordHereAvailability(t.Context(), owner.Authority, match.Resources{}); err != nil {
			t.Fatal(err)
		}
		reserved := match.Resources{Cores: 4 + float64(amount%17)/4}
		binding := owner.Capability.Binding
		actor := Actor{Kind: ActorRunHost, Authority: owner.Authority, CapabilityPrefix: owner.Capability.Prefix, Owner: OwnerLeaseRef{Kind: OwnerTrigger, RunID: "run", Holder: binding.Holder, Generation: binding.Generation}}
		switch attack % 9 {
		case 1:
			reserved.Cores = 9 + float64(amount)
		case 2:
			reserved.Cores = -1 - float64(amount)
		case 3:
			reserved.Cores = math.NaN()
		case 4:
			reserved.MemoryBytes = int64(amount) + 1
		case 5:
			reserved.Cores = math.Inf(1)
		case 6:
			actor.Owner.RunID = "foreign"
		case 7:
			actor.Owner.Generation++
		case 8:
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"set":true,"value":["name=another-machine"]}}`))
		}
		tenant := &Tenant{s: s, team: "a"}
		result, err := tenant.ReconcileNode(t.Context(), ReconcileRequest{Ref: NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, Policy: SourceNone, Reserved: &reserved})
		if attack%9 == 0 {
			if err != nil || result.Kind != Execute || result.Claim == nil {
				t.Fatalf("reserved job did not receive fenced award: %+v %v", result, err)
			}
		} else {
			if result.Kind == Execute || result.Claim != nil {
				t.Fatal("reservation widened capacity, placement or claim authority")
			}
			row, readErr := tenant.ReadNode(t.Context(), NodeRef{RunID: "run", NodeID: "job"})
			if readErr != nil || row.ClaimedBy != "" {
				t.Fatalf("refused reservation consumed a claim: %+v %v", row, readErr)
			}
		}
	})
}
