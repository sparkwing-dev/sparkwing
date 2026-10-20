package store_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestUnitReaperContinuesAfterTargetFailure(t *testing.T) {
	st := storetest.NewSQLite(t).Open(t)
	tenant := tenantFor(t, st, "acme")
	seedExpiredUnitNode(t, st, "acme", "bad", 1, 0)
	seedExpiredUnitNode(t, st, "acme", "good", 2, 0)
	if _, err := st.DB().Exec(`CREATE TRIGGER refuse_reap BEFORE UPDATE ON nodes WHEN OLD.run_id='bad' BEGIN SELECT RAISE(ABORT,'target refused'); END`); err != nil {
		t.Fatal(err)
	}
	lease, err := st.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	page, err := st.AsOperator().ReapUnits(t.Context(), lease, 10)
	if err == nil || !strings.Contains(err.Error(), "target refused") || len(page) != 1 || page[0].RunID != "good" {
		t.Fatalf("reap results=%+v, error=%v", page, err)
	}
	bad, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "bad", NodeID: "job"})
	if err != nil || bad.Status != store.StatusRunning || bad.Disruptions != 0 {
		t.Fatalf("failed target changed: %+v, %v", bad, err)
	}
}

func seedExpiredUnitNode(t *testing.T, st *store.Store, team store.Team, runID string, number, disruptions int) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := st.DB().ExecContext(t.Context(), storetest.Rebind(st, query), args...); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	exec(`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days)
VALUES (?,?,?,'ci','running',?,'request',1,'gh:1','host','execute',?,'account',?,'r30',30)`, team, runID, number, []byte(`{}`), []byte(`[]`), now.Add(-time.Hour).UnixNano())
	exec(`INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,claimed_by,capability_prefix,generation,attempt,disruptions,lease_expires_at,started_at)
VALUES (?,?,'job','running',?,?,'runner','claim',4,1,?,?,?)`, team, runID, []byte(`{"id":"job"}`), []byte(`{}`), disruptions, now.Add(-time.Minute).UnixNano(), now.Add(-time.Hour).UnixNano())
	exec(`INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at)
VALUES (?,?,'job',1,4,'runner','claim','runner','runner','remote',?)`, team, runID, now.Add(-time.Hour).UnixNano())
}

func TestUnitReaperBoundsNodeRecovery(t *testing.T) {
	st := storetest.Open(t)
	tenant := tenantFor(t, st, "acme")
	for i := range 3 {
		seedExpiredUnitNode(t, st, "acme", fmt.Sprintf("run-%d", i), i+1, 0)
	}
	lease, err := st.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, size := range []int{2, 1, 0} {
		page, err := st.AsOperator().ReapUnits(t.Context(), lease, 2)
		if err != nil || len(page) != size {
			t.Fatalf("page: %+v, %v, expected %d units", page, err, size)
		}
		for _, unit := range page {
			if seen[unit.RunID] || unit.Team != "acme" || !unit.Requeued || unit.Disruptions != 1 {
				t.Fatalf("recovered unit: %+v", unit)
			}
			seen[unit.RunID] = true
			row, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID})
			if err != nil || row.Status != store.StatusPending || row.Generation != 4 || row.Attempt != 1 || row.ClaimedBy != "" || row.LeaseExpiresAt != nil || row.ApplicationRetries != 0 {
				t.Fatalf("requeued node: %+v, %v", row, err)
			}
		}
	}
}

func TestUnitReaperPreservesAgentLostAttempt(t *testing.T) {
	for _, disruptions := range []int{0, 2} {
		t.Run(fmt.Sprint(disruptions), func(t *testing.T) {
			st := storetest.Open(t)
			tenant := tenantFor(t, st, "acme")
			seedExpiredUnitNode(t, st, "acme", "run", 1, disruptions)
			lease, err := st.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			page, err := st.AsOperator().ReapUnits(t.Context(), lease, 1)
			if err != nil || len(page) != 1 || page[0].Failed != (disruptions == 2) || page[0].Disruptions != disruptions+1 {
				t.Fatalf("loss policy: %+v, %v", page, err)
			}
			row, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil || row.ClaimedBy != "" || row.LeaseExpiresAt != nil || row.Generation != 4 || row.ApplicationRetries != 0 {
				t.Fatalf("claim survived loss: %+v, %v", row, err)
			}
			if disruptions == 2 && (row.Outcome != store.NodeFailed || row.FinishedAt == nil) {
				t.Fatalf("disruption limit: %+v", row)
			}
			var outcome, reason string
			var finished int64
			if err := st.DB().QueryRowContext(t.Context(), `SELECT outcome,failure_reason,finished_at FROM node_attempts WHERE team='acme' AND run_id='run' AND node_id='job' AND attempt=1`).Scan(&outcome, &reason, &finished); err != nil || outcome != "failed" || reason != "agent_lost" || finished <= 0 {
				t.Fatalf("attempt loss: %s %s at %d, %v", outcome, reason, finished, err)
			}
		})
	}
}
