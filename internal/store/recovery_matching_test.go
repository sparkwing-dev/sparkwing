package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestRequeuedJobRemainsEligibleForEveryMatchingRunner(t *testing.T) {
	for _, claimant := range []string{"runner", "replacement"} {
		for _, queued := range []bool{false, true} {
			t.Run(claimant+map[bool]string{false: "/named", true: "/queue"}[queued], func(t *testing.T) {
				tenant, now, owner := reconcileFixture(t)
				s := tenant.s
				root := authorityRoot("replacement", "a")
				root.Subject.ID = claimant
				root.Grants = []capability.Grant{capability.NodesClaim}
				authorityPersist(t, s, root)
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET expires_at=? WHERE id='run'`, now.Add(time.Hour).UnixNano())
				rewriteSchemaExec(t, s.db, `UPDATE runs SET plan_json=?,plan_hash='plan',spec_json=?,spec_hash='spec',sealed_at=1 WHERE id='run'`, []byte(`{"jobs":[{"id":"job"}]}`), []byte(`{}`))
				indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET lease_expires_at=? WHERE run_id='run'`, now.Add(-time.Nanosecond).UnixNano())
				reaped, err := s.AsOperator().ReapUnits(t.Context(), 10)
				if err != nil || len(reaped) != 1 || !reaped[0].Requeued {
					t.Fatalf("reap: %+v, %v", reaped, err)
				}
				ref := runspec.NodeRef{RunID: "run", NodeID: "job"}
				if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: ref, Actor: owner, To: runstate.NodeReady, Cause: runstate.CauseRelease}); err != nil {
					t.Fatal(err)
				}
				waiting, err := s.AsOperator().ListWaitingNodes(t.Context(), now.Add(time.Nanosecond), [2]string{}, 10)
				if err != nil || len(waiting) != 1 || runspec.RunID(waiting[0].RunID) != ref.RunID || runspec.NodeID(waiting[0].NodeID) != ref.NodeID {
					t.Fatalf("recovered work awaiting a runner: %+v, %v", waiting, err)
				}
				demand := UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}}
				if queued {
					demand.Target, demand.Kinds = nil, []capability.UnitKind{capability.UnitNode}
				}
				claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
				if err != nil || runspec.RunID(claim.RunID) != ref.RunID || runspec.NodeID(claim.NodeID) != ref.NodeID || claim.Award.Holder != claimant || claim.Award.Generation != 3 {
					t.Fatalf("reclaim: %+v, %v", claim, err)
				}
				row, err := tenant.ReadNode(t.Context(), ref)
				if err != nil || row.ClaimedBy != claimant || row.Disruptions != 1 || row.ApplicationRetries != 0 || row.StateSeq != 3 {
					t.Fatalf("recovered row: %+v, %v", row, err)
				}
				events, err := tenant.ReadStateEvents(t.Context(), string(ref.RunID), 0, 10)
				if err != nil || len(events) != 3 || events[0].Kind != "job_requeued" || events[1].Kind != "job_ready" || events[2].Kind != "job_claimed" {
					t.Fatalf("recovery events: %+v, %v", events, err)
				}
				rewriteSchemaCount(t, s.db, "runs", 1)
				waiting, err = s.AsOperator().ListWaitingNodes(t.Context(), now.Add(time.Nanosecond), [2]string{}, 10)
				if err != nil || len(waiting) != 0 {
					t.Fatalf("claimed work awaiting a runner: %+v, %v", waiting, err)
				}
			})
		}
	}
}
