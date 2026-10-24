package store

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestClaimSelectionRejectsEveryInvalidKindBeforeAward(t *testing.T) {
	for _, problem := range []string{"empty", "duplicate", "unknown", "mixed_unauthorized", "named_unauthorized", "named_duplicate"} {
		t.Run(problem, func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			demand.Target = nil
			want := apierr.BadRequest
			switch problem {
			case "duplicate":
				demand.Kinds = []capability.UnitKind{capability.UnitNode, capability.UnitNode}
			case "unknown":
				demand.Kinds = []capability.UnitKind{"job"}
			case "mixed_unauthorized", "named_unauthorized":
				nodesOnly := authorityRoot("nodes-only", "a")
				authorityPersist(t, s, nodesOnly)
				demand.CapabilityPrefix = nodesOnly.Prefix
				demand.Kinds = []capability.UnitKind{capability.UnitNode, capability.UnitPlan}
				want = apierr.Forbidden
				if problem == "named_unauthorized" {
					demand.Target = &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}
				}
			case "named_duplicate":
				demand.Kinds = []capability.UnitKind{capability.UnitNode, capability.UnitNode}
				demand.Target = &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}
			}
			_, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != want {
				t.Fatalf("selection %s: %v, want %s", problem, err, want)
			}
			rewriteSchemaCount(t, s.db, "billing_awards", 0)
			rewriteSchemaCount(t, s.db, "events", 0)
		})
	}
}

func TestMixedClaimSelectionUsesOneRankedQueue(t *testing.T) {
	for _, first := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		t.Run(string(first), func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			demand.Target = nil
			demand.Kinds = []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}
			maintenanceRun(t, s, "other", 2, runstate.RunRunning)
			rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) SELECT team,'other',unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels FROM triggers WHERE id='run'`)
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET status='done',finished_at=? WHERE id='other'`, s.now().UnixNano())
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET run_id='other'`)
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET available_at=2 WHERE id='run'`)
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=3 WHERE run_id='other'`)
			if first == capability.UnitNode {
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET available_at=4 WHERE id='run'`)
			}
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil || claim.Kind != first {
				t.Fatalf("ordered mixed claim: %+v, %v", claim, err)
			}
		})
	}
}
