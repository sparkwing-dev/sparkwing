package store

import (
	"database/sql"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func renewalFixture(t *testing.T, kind capability.UnitKind) (*Store, UnitDemand, UnitClaim, *time.Time) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	now := s.now()
	s.clock = func() time.Time { return now }
	demand.Target = &UnitRef{Kind: kind, RunID: "run"}
	if kind == capability.UnitNode {
		demand.Target.NodeID = "job"
	}
	if kind == capability.UnitPlan || kind == capability.UnitEval {
		root := authorityRoot("launcher", "")
		root.Subject.Kind = capability.SubjectService
		root.Grants = []capability.Grant{capability.ClaimsLaunch}
		authorityPersist(t, s, root)
		demand.CapabilityPrefix = root.Prefix
		rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
		rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
	}
	if kind == capability.UnitEval {
		demand.Target.NodeID, demand.Target.SourceGeneration = "job", 7
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1,generation=7,expansion='expected'`)
		rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','controller',?,3)`, now.Add(time.Hour).UnixNano())
		rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,owner_generation) VALUES ('a','run','job',7,'pending',3)`)
	}
	claim, err := s.ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	if kind == capability.UnitPlan || kind == capability.UnitEval {
		issued, err := s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
		if err != nil {
			t.Fatal(err)
		}
		claim.Award.CapabilityPrefix = issued.Capability.Prefix
	}
	return s, demand, claim, &now
}

func TestRenewUnitRecordsOwnerExecutionForRecovery(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, claim, now := renewalFixture(t, kind)
			query := `SELECT started_at FROM triggers WHERE id='run'`
			if kind == capability.UnitEval {
				query = `SELECT started_at FROM eval_units`
			}
			if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
				t.Fatal(err)
			}
			var started sql.NullInt64
			if err := s.db.QueryRow(query).Scan(&started); err != nil || started.Valid {
				t.Fatalf("idle renewal started unit: %v, %v", started, err)
			}
			*now = now.Add(time.Second)
			firstStart := now.UnixNano()
			var beat LeaseBeat
			for range 2 {
				var err error
				beat, err = s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
				if err != nil {
					t.Fatal(err)
				}
				*now = now.Add(time.Second)
			}
			if err := s.db.QueryRow(query).Scan(&started); err != nil || !started.Valid || started.Int64 != firstStart {
				t.Fatalf("execution start changed: %v, %v", started, err)
			}
			*now = beat.ExpiresAt
			lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			reaped, err := s.ReapUnits(t.Context(), lease, 10)
			if err != nil || len(reaped) != 1 || !reaped[0].Requeued || reaped[0].Disruptions != 1 || reaped[0].Generation != claim.Award.Generation {
				t.Fatalf("started unit loss: %+v, %v", reaped, err)
			}
			if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err == nil {
				t.Fatal("reaped owner renewed")
			}
		})
	}
}

func TestRenewUnitRefusesStaleAndRootAuthority(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		for _, problem := range []string{"root", "revoked", "expired", "generation", "holder"} {
			t.Run(string(kind)+"/"+problem, func(t *testing.T) {
				s, demand, claim, now := renewalFixture(t, kind)
				table, predicate := "triggers", `id='run'`
				if kind == capability.UnitEval {
					table, predicate = "eval_units", `run_id='run'`
				}
				if kind == capability.UnitNode {
					table, predicate = "nodes", `run_id='run'`
				}
				prefix := claim.Award.CapabilityPrefix
				switch problem {
				case "root":
					prefix = demand.CapabilityPrefix
				case "revoked":
					rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, demand.CapabilityPrefix)
				case "expired":
					*now = claim.Award.LeaseExpiresAt
				case "generation":
					rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET generation=generation+1 WHERE `+predicate)
				case "holder":
					rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET claimed_by='successor' WHERE `+predicate)
				}
				if _, err := s.RenewUnit(t.Context(), prefix, false); err == nil {
					t.Fatal("invalid unit renewed")
				}
				var lease int64
				if err := s.db.QueryRow(`SELECT lease_expires_at FROM ` + table + ` WHERE ` + predicate).Scan(&lease); err != nil || lease != claim.Award.LeaseExpiresAt.UnixNano() {
					t.Fatalf("refused renewal changed lease: %d, %v", lease, err)
				}
			})
		}
	}
}

func TestRenewUnitDeliversCancellationWithoutStarting(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, claim, now := renewalFixture(t, kind)
			rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=? WHERE id='run'`, now.UnixNano())
			beat, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
			if err != nil || !beat.CancelRequested || !beat.ExpiresAt.After(*now) {
				t.Fatalf("cancelled renewal: %+v, %v", beat, err)
			}
			query := `SELECT started_at FROM triggers WHERE id='run'`
			if kind == capability.UnitEval {
				query = `SELECT started_at FROM eval_units`
			}
			if kind == capability.UnitNode {
				query = `SELECT started_at FROM nodes`
			}
			var started sql.NullInt64
			if err := s.db.QueryRow(query).Scan(&started); err != nil || started.Valid {
				t.Fatalf("cancelled unit started: %v, %v", started, err)
			}
		})
	}
}

func TestRenewUnitSeesOwnerCancellation(t *testing.T) {
	s, _, claim, now := renewalFixture(t, capability.UnitTrigger)
	rewriteSchemaExec(t, s.db, `UPDATE triggers SET cancel_requested_at=? WHERE id='run'`, now.UnixNano())
	beat, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
	if err != nil || !beat.CancelRequested {
		t.Fatalf("owner cancellation: %+v, %v", beat, err)
	}
	var started sql.NullInt64
	if err := s.db.QueryRow(`SELECT started_at FROM triggers`).Scan(&started); err != nil || started.Valid {
		t.Fatalf("cancelled owner started: %v, %v", started, err)
	}
}

func TestRenewUnitNodeRequiresCommittedStart(t *testing.T) {
	s, _, claim, now := renewalFixture(t, capability.UnitNode)
	if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err == nil {
		t.Fatal("renewal invented a job start")
	}
	tenant := &Tenant{s: s, team: claim.Team}
	actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{claim.RunID, claim.NodeID}, To: NodeRunning, Cause: CauseStart, Actor: actor, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	beat, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
	if err != nil || !beat.ExpiresAt.After(claim.Award.LeaseExpiresAt) {
		t.Fatalf("running job renewal: %+v, %v", beat, err)
	}
	rewriteSchemaCount(t, s.db, "node_attempts", 1)
}

func TestRenewUnitEvalChecksBothGenerations(t *testing.T) {
	for _, problem := range []string{"source_generation", "source_outcome", "expansion_accepted", "controller_generation", "controller_expiry"} {
		t.Run(problem, func(t *testing.T) {
			s, _, claim, now := renewalFixture(t, capability.UnitEval)
			switch problem {
			case "source_generation":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=generation+1`)
			case "source_outcome":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET outcome='failed'`)
			case "expansion_accepted":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET expansion='accepted'`)
			case "controller_generation":
				rewriteSchemaExec(t, s.db, `UPDATE leases SET generation=generation+1 WHERE name='controller.owner'`)
			case "controller_expiry":
				rewriteSchemaExec(t, s.db, `UPDATE leases SET until=? WHERE name='controller.owner'`, now.UnixNano())
			}
			if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err == nil {
				t.Fatal("ended evaluation renewed")
			}
			var started sql.NullInt64
			if err := s.db.QueryRow(`SELECT started_at FROM eval_units`).Scan(&started); err != nil || started.Valid {
				t.Fatalf("invalid evaluation started: %v, %v", started, err)
			}
		})
	}
}

func TestRenewUnitRequiresBootstrapExchange(t *testing.T) {
	s, issued := adapterBootstrap(t)
	if _, err := s.RenewUnit(t.Context(), issued.Capability.Prefix, false); err == nil {
		t.Fatal("bootstrap renewed its unit")
	}
	exchanged, err := s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: issued.Capability.Prefix})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenewUnit(t.Context(), exchanged.Capability.Prefix, false); err != nil {
		t.Fatal(err)
	}
}
