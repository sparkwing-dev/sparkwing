package store

import (
	"database/sql"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestCloudCapacityFactsFollowAwardPlacementAndActualStart(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand, claim, now := launcherSnapshotFixture(t, kind)
			root, fence := launcherMeteredFence(t, s, demand, claim, kind)
			op := s.AsOperator()
			var requested string
			var latency sql.NullInt64
			if err := s.db.QueryRowContext(t.Context(), `SELECT requested_class,claim_to_start_ns FROM cloud_job_capacity WHERE team='a'`).Scan(&requested, &latency); err != nil || requested != "sparkwing-2cpu" || latency.Valid {
				t.Fatalf("claim facts: %q %+v %v", requested, latency, err)
			}
			*now = now.Add(2 * time.Second)
			waiting := []capability.LauncherObservation{{Award: fence, Reservation: true, WaitedForFreshNode: true}}
			if _, _, err := op.ReconcileLauncherAwards(t.Context(), root, waiting, []capability.LeaseRef{fence}, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(3 * time.Second)
			activation := capability.LauncherActivation{Award: fence, NodeUID: "node", ReservationID: "reservation", Capacity: &capability.CloudCapacityGrant{RequestedClass: requested, GrantedClass: "sparkwing-4cpu"}}
			observation := []capability.LauncherObservation{{Award: fence}}
			if _, _, err := op.ReconcileLauncherAwards(t.Context(), root, observation, nil, nil, nil, []capability.LauncherActivation{activation}); err != nil {
				t.Fatal(err)
			}
			activation.Capacity.GrantedClass = requested
			if _, _, err := op.ReconcileLauncherAwards(t.Context(), root, observation, nil, nil, nil, []capability.LauncherActivation{activation}); err == nil {
				t.Fatal("changed committed hardware class")
			}
			activation.Capacity.GrantedClass = "sparkwing-4cpu"
			var granted string
			var waited bool
			if err := s.db.QueryRowContext(t.Context(), `SELECT granted_class,waited_for_fresh_node,claim_to_start_ns FROM cloud_job_capacity WHERE team='a'`).Scan(&granted, &waited, &latency); err != nil || granted != "sparkwing-4cpu" || !waited || latency.Valid {
				t.Fatalf("placement facts: %q %v %+v %v", granted, waited, latency, err)
			}
			issued, err := op.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
			if err != nil {
				t.Fatal(err)
			}
			*now = now.Add(7 * time.Second)
			if _, err := op.RenewUnit(t.Context(), issued.Capability.Prefix, false); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRowContext(t.Context(), `SELECT claim_to_start_ns FROM cloud_job_capacity WHERE team='a'`).Scan(&latency); err != nil || !latency.Valid || latency.Int64 != int64(12*time.Second) {
				t.Fatalf("actual start latency: %+v %v", latency, err)
			}
			*now = now.Add(time.Second)
			if _, err := op.RenewUnit(t.Context(), issued.Capability.Prefix, false); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRowContext(t.Context(), `SELECT claim_to_start_ns FROM cloud_job_capacity WHERE team='a'`).Scan(&latency); err != nil || latency.Int64 != int64(12*time.Second) {
				t.Fatalf("renewal changed first start: %+v %v", latency, err)
			}
			var priceClass int64
			if err := s.db.QueryRowContext(t.Context(), `SELECT cpu_class FROM credit_charges WHERE team='a' AND kind='reservation'`).Scan(&priceClass); err != nil || priceClass != 2 {
				t.Fatalf("upsized award billing class: %d %v", priceClass, err)
			}
		})
	}
}

func TestCloudCapacityGrantCannotChangeCommittedRequestedOrGrantedClass(t *testing.T) {
	for _, wrong := range []*capability.CloudCapacityGrant{
		nil,
		{RequestedClass: "sparkwing-4cpu", GrantedClass: "sparkwing-4cpu"},
		{RequestedClass: "sparkwing-2cpu", GrantedClass: "sparkwing-8cpu"},
	} {
		name := "missing"
		if wrong != nil {
			name = wrong.RequestedClass + wrong.GrantedClass
		}
		t.Run(name, func(t *testing.T) {
			s, demand, claim, _ := launcherSnapshotFixture(t, capability.UnitNode)
			root, fence := launcherMeteredFence(t, s, demand, claim, capability.UnitNode)
			activation := capability.LauncherActivation{Award: fence, NodeUID: "node", ReservationID: "reservation", Capacity: wrong}
			if _, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, []capability.LauncherObservation{{Award: fence}}, nil, nil, nil, []capability.LauncherActivation{activation}); err == nil {
				t.Fatal("accepted wrong allocation")
			}
		})
	}
}

func TestCloudUpsizeSwitchDefaultsOnAndRevalidatesOperator(t *testing.T) {
	s, demand, claim, now := launcherSnapshotFixture(t, capability.UnitNode)
	root, fence := launcherMeteredFence(t, s, demand, claim, capability.UnitNode)
	op := s.AsOperator()
	if enabled, err := op.CloudUpsizeEnabled(t.Context()); err != nil || !enabled {
		t.Fatalf("default: %v %v", enabled, err)
	}
	operator := authorityRoot("ops", "")
	operator.Subject.Kind = capability.SubjectService
	operator.Grants = []capability.Grant{capability.OperatorLimits}
	expiry := now.Add(time.Minute)
	operator.ExpiresAt = &expiry
	authorityPersist(t, s, operator)
	a := capability.Authorized{Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Prefix}}}
	if err := op.WriteCloudUpsizeEnabled(t.Context(), a, false); err != nil {
		t.Fatal(err)
	}
	if enabled, err := op.CloudUpsizeEnabled(t.Context()); err != nil || enabled {
		t.Fatalf("disabled: %v %v", enabled, err)
	}
	activation := capability.LauncherActivation{Award: fence, NodeUID: "node", ReservationID: "reservation", Capacity: &capability.CloudCapacityGrant{RequestedClass: "sparkwing-2cpu", GrantedClass: "sparkwing-4cpu"}}
	observation := []capability.LauncherObservation{{Award: fence}}
	if _, _, err := op.ReconcileLauncherAwards(t.Context(), root, observation, nil, nil, nil, []capability.LauncherActivation{activation}); err == nil {
		t.Fatal("upsized with operator switch off")
	}
	activation.Capacity.GrantedClass = activation.Capacity.RequestedClass
	if awards, _, err := op.ReconcileLauncherAwards(t.Context(), root, observation, nil, nil, nil, []capability.LauncherActivation{activation}); err != nil || len(awards) != 1 || awards[0].CloudUpsizeEnabled {
		t.Fatalf("exact class while disabled: %+v %v", awards, err)
	}
	*now = now.Add(2 * time.Minute)
	if err := op.WriteCloudUpsizeEnabled(t.Context(), a, true); err == nil {
		t.Fatal("changed switch after operator authority expired")
	}
	if enabled, err := op.CloudUpsizeEnabled(t.Context()); err != nil || enabled {
		t.Fatalf("refused mutation changed switch: %v %v", enabled, err)
	}
}
