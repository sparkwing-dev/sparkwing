package store

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestOwnerReaperUsesRenewedLeaseWithoutJobActivity(t *testing.T) {
	for _, hasJob := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_jobs", true: "inactive_job"}[hasJob], func(t *testing.T) {
			s, _, claim, now := renewalFixture(t, capability.UnitTrigger)
			rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?`, []byte(`{"request":{"origin":"local"}}`))
			if !hasJob {
				rewriteSchemaExec(t, s.db, `DELETE FROM nodes`)
			}
			beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
			if err != nil {
				t.Fatal(err)
			}
			firstExpiry := beat.ExpiresAt
			*now = now.Add(firstExpiry.Sub(*now) / 2)
			beat, err = s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
			if err != nil || !beat.ExpiresAt.After(firstExpiry) {
				t.Fatalf("owner renewal: %+v, %v", beat, err)
			}
			*now = firstExpiry
			if reaped, err := s.AsOperator().ReapUnits(t.Context(), 10); err != nil || len(reaped) != 0 {
				t.Fatalf("live owner reaped: %+v, %v", reaped, err)
			}
			*now = beat.ExpiresAt
			reaped, err := s.AsOperator().ReapUnits(t.Context(), 10)
			if err != nil || len(reaped) != 1 || !reaped[0].Failed || reaped[0].Requeued {
				t.Fatalf("expired local owner: %+v, %v", reaped, err)
			}
			run, err := (&Tenant{s: s, team: claim.Team}).ReadRun(t.Context(), claim.RunID)
			if err != nil || run.State != runstate.RunFailed {
				t.Fatalf("lost local run: %+v, %v", run, err)
			}
		})
	}
}

func TestOwnerReaperJobHeartbeatCannotReplaceOwnerLease(t *testing.T) {
	tenant, now := ownerReaperFixture(t, true, 0, runspec.OriginLocal)
	for _, id := range []string{"build", "test", "deploy"} {
		rewriteSchemaExec(t, tenant.s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,last_heartbeat) VALUES ('a','run',?,'pending',?,?,?)`, id, []byte(`{"id":"`+id+`"}`), []byte(`{}`), now.UnixNano())
	}
	for i := range 2 {
		reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), 10)
		if err != nil || len(reaped) != 1-i {
			t.Fatalf("reap %d: %+v, %v", i, reaped, err)
		}
	}
	for _, id := range []string{"build", "test", "deploy"} {
		node, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: runspec.NodeID(id)})
		if err != nil || node.Outcome != runstate.NodeCancelled || node.FinishedAt == nil || node.StateSeq != 1 {
			t.Fatalf("orphaned dependent %s: %+v, %v", id, node, err)
		}
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != runstate.RunFailed {
		t.Fatalf("orphaned local run: %+v, %v", run, err)
	}
	events, err := tenant.ReadStateEvents(t.Context(), "run", 0, 100)
	if err != nil || len(events) != 4 {
		t.Fatalf("terminal events replayed: %+v, %v", events, err)
	}
	var payload struct{ Reason string }
	if err := json.Unmarshal(events[len(events)-1].Payload, &payload); err != nil || payload.Reason != "agent_lost" {
		t.Fatalf("owner loss reason: %+v, %v", payload, err)
	}
}
