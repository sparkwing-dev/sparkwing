package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestReleaseUnitOwnerFenceProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(57, 813))
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			s, claim := releaseFixture(t, kind)
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='pending',claimed_by=NULL,lease_expires_at=NULL,capability_prefix='',attempt=0`)
			tenant := &Tenant{s: s, team: claim.Team}
			before, err := tenant.ReadSnapshot(t.Context(), claim.RunID)
			if err != nil {
				t.Fatal(err)
			}
			var query string
			if kind == capability.UnitEval {
				query = `SELECT status,generation,claimed_by,capability_prefix,lease_expires_at,started_at FROM eval_units`
			} else {
				query = `SELECT status,generation,claimed_by,capability_prefix,lease_expires_at,started_at FROM triggers`
			}
			unitState := func() []any {
				var status, prefix string
				var generation int64
				var holder sql.NullString
				var lease, start sql.NullInt64
				if err := s.db.QueryRowContext(t.Context(), query).Scan(&status, &generation, &holder, &prefix, &lease, &start); err != nil {
					t.Fatal(err)
				}
				return []any{status, generation, holder, prefix, lease, start}
			}
			unit := unitState()
			for i := range 64 {
				forged, prefix := claim, claim.Award.CapabilityPrefix
				suffix := fmt.Sprintf("/%x", random.Uint64())
				switch i % 8 {
				case 0:
					forged.Team += Team(suffix)
				case 1:
					forged.RunID += suffix
				case 2:
					forged.SourceGeneration += 1 + random.Int64N(1000)
				case 3:
					forged.Award.Generation += 1 + random.Int64N(1000)
				case 4:
					forged.Award.Holder += suffix
				case 5:
					forged.Award.CapabilityPrefix += suffix
				case 6:
					prefix += suffix
				case 7:
					forged.NodeID += suffix
				}
				if err := s.ReleaseUnit(t.Context(), forged, prefix); err == nil {
					t.Fatalf("forged owner release accepted: %+v", forged)
				}
				if !reflect.DeepEqual(unit, unitState()) {
					t.Fatal("refused release changed the owner unit")
				}
			}
			after, err := tenant.ReadSnapshot(t.Context(), claim.RunID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refused release changed run state: %v", err)
			}
			rewriteSchemaCount(t, s.db, "billing_awards", 1)
			rewriteSchemaCount(t, s.db, "credit_charges", 0)
			if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func releaseFixture(t *testing.T, kind capability.UnitKind) (*Store, UnitClaim) {
	t.Helper()
	s := authorityStore(t)
	s.clock = func() time.Time { return time.Unix(1_800_000_000, 0) }
	root := authorityRoot("machine", "a")
	root.Grants = []capability.Grant{capability.NodesClaim, capability.TriggersClaim, capability.ClaimsLaunch}
	award := authorityClaimFixture(t, s, root)
	unit := UnitRef{Kind: kind, RunID: "run"}
	purpose := capability.PurposeTrigger
	switch kind {
	case capability.UnitNode:
		unit.NodeID, purpose = "job", capability.PurposeWork
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='pending',started_at=NULL WHERE run_id='run'`)
	case capability.UnitPlan:
		purpose = capability.PurposePlan
	case capability.UnitEval:
		unit.NodeID, unit.SourceGeneration, purpose = "job", 2, capability.PurposeEval
		rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,generation,expires_at,owner_generation) VALUES ('a','run','job',2,'pending',6,?,1)`, s.now().Add(time.Hour).UnixNano())
	}
	if kind == capability.UnitTrigger || kind == capability.UnitPlan {
		rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind=?,generation=6 WHERE id='run'`, kind)
	}
	id, err := encodeAwardUnit(unit)
	if err != nil {
		t.Fatal(err)
	}
	award.Unit, award.UnitID, award.Purpose = kind, id, purpose
	var issued capability.Issued
	err = s.inTx(t.Context(), func(tx *storeTx) error {
		var err error
		issued, err = issueFixtureAward(t, s, tx, award)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	c := issued.Capability
	if kind == capability.UnitNode {
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='pending',started_at=NULL WHERE run_id='run'`)
	}
	return s, UnitClaim{Team: "a", Kind: kind, RunID: unit.RunID, NodeID: unit.NodeID, SourceGeneration: unit.SourceGeneration, Award: ClaimAward{CapabilityPrefix: c.Prefix, Holder: "machine", Generation: 7}}
}

func TestReleaseUnitReturnsUnstartedWorkWithoutAttempt(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, claim := releaseFixture(t, kind)
			if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
				t.Fatal(err)
			}
			var status string
			var generation int64
			var query string
			switch kind {
			case capability.UnitNode:
				query = `SELECT status,generation FROM nodes WHERE run_id='run'`
			case capability.UnitEval:
				query = `SELECT status,generation FROM eval_units`
			default:
				query = `SELECT status,generation FROM triggers WHERE id='run'`
			}
			if err := s.db.QueryRow(query).Scan(&status, &generation); err != nil || status != "pending" || generation != 7 {
				t.Fatalf("released state = %s %d, %v", status, generation, err)
			}
			rewriteSchemaCount(t, s.db, "node_attempts", 0)
			if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err == nil {
				t.Fatal("cleared holder released again")
			}
		})
	}
}

func TestReleaseUnitRefusesForgedStartedAndCancelledClaims(t *testing.T) {
	for _, problem := range []string{"team", "run", "generation", "holder", "prefix", "started", "cancelled", "expired", "revoked_parent"} {
		t.Run(problem, func(t *testing.T) {
			s, claim := releaseFixture(t, capability.UnitNode)
			prefix := claim.Award.CapabilityPrefix
			switch problem {
			case "team":
				claim.Team = "b"
			case "run":
				claim.RunID = "foreign"
			case "generation":
				claim.Award.Generation++
			case "holder":
				claim.Award.Holder = "foreign"
			case "prefix":
				prefix = "machine"
			case "started":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='running',started_at=1 WHERE run_id='run'`)
			case "cancelled":
				rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=1 WHERE id='run'`)
			case "expired":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET lease_expires_at=1 WHERE run_id='run'`)
			case "revoked_parent":
				revokeClaimRoot(t, s, "machine", "a")
			}
			err := s.ReleaseUnit(t.Context(), claim, prefix)
			if problem == "cancelled" {
				var refused *apierr.Error
				if !errors.As(err, &refused) || refused.Code != apierr.LockHeld {
					t.Fatalf("cancellation refusal = %v", err)
				}
			} else if problem == "revoked_parent" {
				var refused *apierr.Error
				if !errors.As(err, &refused) || refused.Code != apierr.Unauthenticated {
					t.Fatalf("revocation refusal = %v", err)
				}
			} else {
				var refused *ErrIllegalTransition
				if !errors.As(err, &refused) || refused.Reason != ReasonHolderRights {
					t.Fatalf("release refusal = %v", err)
				}
			}
			var holder string
			if err := s.db.QueryRow(`SELECT claimed_by FROM nodes WHERE run_id='run'`).Scan(&holder); err != nil || holder != "machine" {
				t.Fatalf("refused release changed holder: %q, %v", holder, err)
			}
		})
	}
}

func TestReleaseUnitCannotRequeueCancelledOwnerWork(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			s, claim := releaseFixture(t, kind)
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1,claimed_by=NULL,lease_expires_at=NULL,capability_prefix=''`)
			operator := authorityRoot("operator", "a")
			operator.Grants = []capability.Grant{capability.RunsControl}
			authorityPersist(t, s, operator)
			tenant := &Tenant{s: s, team: "a"}
			_, err := tenant.TransitionRun(t.Context(), RunTransition{RunID: claim.RunID, To: RunCancelled, Cause: RunCancel, Actor: Actor{Kind: ActorOperator, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Prefix}, CapabilityPrefix: operator.Prefix}})
			if err != nil {
				t.Fatal(err)
			}
			before, err := tenant.ReadRun(t.Context(), claim.RunID)
			if err != nil || before.CancelRequestedAt == nil {
				t.Fatalf("cancellation = %+v, %v", before, err)
			}
			beforeEvents, err := tenant.ReadStateEvents(t.Context(), claim.RunID, 0, 10)
			if err != nil || len(beforeEvents) == 0 {
				t.Fatalf("cancellation events = %+v, %v", beforeEvents, err)
			}
			err = s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix)
			var denied *apierr.Error
			if !errors.As(err, &denied) || denied.Code != apierr.LockHeld {
				t.Fatalf("cancelled owner release = %v", err)
			}
			events, err := tenant.ReadStateEvents(t.Context(), claim.RunID, beforeEvents[len(beforeEvents)-1].Seq, 10)
			if err != nil || len(events) != 0 {
				t.Fatalf("refused release events = %+v, %v", events, err)
			}
		})
	}
}
