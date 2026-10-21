package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestIgnoredCancellationCannotRenewBeyondDrain(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitNode, capability.UnitTrigger, capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, claim, now := renewalFixture(t, kind)
			tenant := &Tenant{s: s, team: "a"}
			if kind == capability.UnitNode {
				principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
				if err != nil {
					t.Fatal(err)
				}
				_, actor, err := renewalActor(principal.Capability)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tenant.TransitionNode(t.Context(), StartNode{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, SourceAttempt: sourceAttempt(0)}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err != nil {
				t.Fatal(err)
			}
			operator := authorityMember(t, s, "cancel-operator", RoleOwner)
			operator.Kind = ActorOperator
			started := *now
			rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a','global:drain',1,1)`)
			rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at) VALUES ('a','global:drain','held','run','',?,?)`, now.UnixNano(), now.Add(time.Hour).UnixNano())
			if _, err := tenant.TransitionRun(t.Context(), RunTransition{RunID: "run", To: RunCancelled, Cause: RunCancel, Actor: operator, Reason: "user_cancelled"}); err != nil {
				t.Fatal(err)
			}
			deadline := started.Add(5 * time.Minute)
			var persisted int64
			if err := s.db.QueryRow(`SELECT drain_deadline_at FROM runs WHERE team='a' AND id='run'`).Scan(&persisted); err != nil || persisted != deadline.UnixNano() {
				t.Fatalf("deadline=%d, want %d: %v", persisted, deadline.UnixNano(), err)
			}
			for elapsed := 30 * time.Second; elapsed < 5*time.Minute; elapsed += 30 * time.Second {
				*now = started.Add(elapsed)
				if elapsed == 2*time.Minute {
					if _, err := tenant.TransitionRun(t.Context(), RunTransition{RunID: "run", To: RunCancelled, Cause: RunCancel, Actor: operator, Reason: "repeated cancellation"}); err != nil {
						t.Fatal(err)
					}
					if err := s.db.QueryRow(`SELECT drain_deadline_at FROM runs WHERE team='a' AND id='run'`).Scan(&persisted); err != nil || persisted != deadline.UnixNano() {
						t.Fatalf("repeat moved drain: %d %v", persisted, err)
					}
				}

				beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
				if err != nil || !beat.CancelRequested || beat.ExpiresAt.After(deadline) {
					t.Fatalf("renew at %s: %+v %v, deadline %v", elapsed, beat, err, deadline)
				}
				for _, table := range []string{"nodes", "triggers", "eval_units", "concurrency_holders", "concurrency_waiters"} {
					var count int
					if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE lease_expires_at>?`, deadline.UnixNano()).Scan(&count); err != nil || count != 0 {
						t.Fatalf("%s leases escaped drain: %d %v", table, count, err)
					}
				}
			}
			*now = deadline
			if beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err == nil {
				t.Fatalf("ignored cancel renewed past deadline: %+v", beat)
			}
			lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			reaped, err := s.AsOperator().ReapUnits(t.Context(), lease, 10)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, unit := range reaped {
				if unit.Kind == string(kind) && unit.Cancelled {
					found = true
				}
			}
			if !found {
				t.Fatalf("ignored cancel not reaped: %+v", reaped)
			}
		})
	}
}

func TestLeaseBoundReferenceModel(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	for capabilitySeconds := 0; capabilitySeconds <= 600; capabilitySeconds += 37 {
		for drainSeconds := 0; drainSeconds <= 300; drainSeconds += 29 {
			for fundedSeconds := 0; fundedSeconds <= 200; fundedSeconds += 43 {
				bounds := []time.Time{now.Add(time.Duration(capabilitySeconds) * time.Second), now.Add(time.Duration(drainSeconds) * time.Second), now.Add(time.Duration(fundedSeconds) * time.Second)}
				expiry, err := leaseBound(now, bounds...)
				if capabilitySeconds == 0 || drainSeconds == 0 || fundedSeconds == 0 {
					if err == nil {
						t.Fatal("expired bound renewed")
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				want := now.Add(3 * time.Minute)
				for _, bound := range bounds {
					if bound.Before(want) {
						want = bound
					}
				}
				if !expiry.Equal(want) {
					t.Fatalf("lease %v, want earliest bound %v", expiry, want)
				}
				for _, bound := range bounds {
					if expiry.After(bound) {
						t.Fatalf("lease %v exceeds bound %v", expiry, bound)
					}
				}
			}
		}
	}
}

func TestControllerOwnerCannotRenewPastCancellationDrain(t *testing.T) {
	tenant, request, now := controllerPlanFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}}})
	controllerJobActor(t, tenant, request.RunID)
	row, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || row.Owner == nil {
		t.Fatalf("controller owner: %+v %v", row.Owner, err)
	}
	owner := Actor{Kind: ActorController, Owner: *row.Owner}
	operator := authorityMember(t, tenant.s, "cancel-controller", RoleOwner)
	operator.Kind = ActorOperator
	started := *now
	for _, table := range []string{"holders", "waiters"} {
		key := "global:drain-" + table
		rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a',?,1,1)`, key)
		if table == "holders" {
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at) VALUES ('a',?,'held',?,'',?,?)`, key, row.ID, now.UnixNano(), now.Add(time.Hour).UnixNano())
		} else {
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,policy,lease_expires_at) VALUES ('a',?,'waiting',?,'',?,'queue',?)`, key, row.ID, now.UnixNano(), now.Add(time.Hour).UnixNano())
		}
	}
	if _, err := tenant.TransitionRun(t.Context(), RunTransition{RunID: runspec.RunID(row.ID), To: RunCancelled, Cause: RunCancel, Actor: operator}); err != nil {
		t.Fatal(err)
	}
	deadline := started.Add(5 * time.Minute)
	for elapsed := time.Minute; elapsed < 5*time.Minute; elapsed += time.Minute {
		*now = started.Add(elapsed)
		beat, err := tenant.RenewOwner(t.Context(), owner)
		if err != nil || !beat.CancelRequested || beat.ExpiresAt.After(deadline) {
			t.Fatalf("controller renewal: %+v %v", beat, err)
		}
		lease := SingletonLease{Name: owner.Owner.Name, Holder: owner.Owner.Holder, Generation: int64(owner.Owner.Generation)}
		if _, err := tenant.s.AsOperator().ReconcileControllerRuns(t.Context(), lease, "", 10); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"concurrency_holders", "concurrency_waiters"} {
			var expiry int64
			if err := tenant.s.db.QueryRow(`SELECT lease_expires_at FROM `+table+` WHERE run_id=?`, row.ID).Scan(&expiry); err != nil || expiry > deadline.UnixNano() {
				t.Fatalf("%s renewed beyond stop deadline: %d, deadline %d: %v", table, expiry, deadline.UnixNano(), err)
			}
		}
	}
	*now = deadline
	if beat, err := tenant.RenewOwner(t.Context(), owner); err == nil {
		t.Fatalf("controller renewed beyond drain: %+v", beat)
	}
}
