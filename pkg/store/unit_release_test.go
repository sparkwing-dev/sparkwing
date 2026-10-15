package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

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
		rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,generation,claimed_by,lease_expires_at,expires_at,owner_generation) VALUES ('a','run','job',2,'claimed',7,'machine',?,?,1)`, s.now().Add(time.Hour).UnixNano(), s.now().Add(time.Hour).UnixNano())
	}
	if kind == capability.UnitTrigger || kind == capability.UnitPlan {
		rewriteSchemaExec(t, s.db, `UPDATE triggers SET status='claimed',unit_kind=?,generation=7,claimed_by='machine',lease_expires_at=? WHERE id='run'`, kind, s.now().Add(time.Hour).UnixNano())
	}
	id, err := encodeAwardUnit(unit)
	if err != nil {
		t.Fatal(err)
	}
	award.Unit, award.UnitID, award.Purpose = kind, id, purpose
	var issued capability.Issued
	err = s.inTx(t.Context(), func(tx *storeTx) error {
		var err error
		issued, err = s.Issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Award, Award: &award})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	c := issued.Capability
	switch kind {
	case capability.UnitNode:
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET capability_prefix=? WHERE run_id='run'`, c.Prefix)
	case capability.UnitEval:
		rewriteSchemaExec(t, s.db, `UPDATE eval_units SET capability_prefix=?`, c.Prefix)
	default:
		rewriteSchemaExec(t, s.db, `UPDATE triggers SET capability_prefix=? WHERE id='run'`, c.Prefix)
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
				rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix='machine'`)
			}
			if err := s.ReleaseUnit(t.Context(), claim, prefix); err == nil {
				t.Fatal("invalid release accepted")
			}
			var holder string
			if err := s.db.QueryRow(`SELECT claimed_by FROM nodes WHERE run_id='run'`).Scan(&holder); err != nil || holder != "machine" {
				t.Fatalf("refused release changed holder: %q, %v", holder, err)
			}
		})
	}
}
