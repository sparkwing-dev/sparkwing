package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A node row can already carry a lineage root naming another team's run, so
// the attempt history read through that root keeps to the node's own team.
func TestExecutionAttemptsStayInsideTheNodesTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	other := tenantFor(t, st, "other")
	seedTenantRun(t, acme, "run-acme", "demo")
	seedTenantRun(t, other, "run-other", "demo")
	for _, run := range []string{"run-acme", "run-other"} {
		if err := st.CreateNode(ctx, store.Node{RunID: run, NodeID: "build", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st, `INSERT INTO node_execution_attempts
  (team, lineage_root_run_id, run_id, node_id, attempt_ordinal, claim_generation, coordinator_id, membership_id,
   executor_kind, executor_name, executor_id, executor_location, holder_id, reservation_id, started_at,
   outcome, failure_reason)
VALUES ('acme', 'run-acme', 'run-acme', 'build', 1, 1, 'coord', 'member', 'runner', 'acme-box', 'exec', 'self-hosted',
   'holder', 'res', ?, 'failed', 'acme private failure')`), time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
		`UPDATE nodes SET retry_root_run_id = 'run-acme' WHERE run_id = 'run-other' AND node_id = 'build'`)); err != nil {
		t.Fatal(err)
	}

	own, err := st.ListNodeExecutionAttempts(ctx, "run-acme", "build")
	if err != nil || len(own) != 1 || own[0].FailureReason != "acme private failure" {
		t.Fatalf("acme's own attempts = %+v, %v", own, err)
	}
	foreign, err := st.ListNodeExecutionAttempts(ctx, "run-other", "build")
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign) != 0 {
		t.Fatalf("a node naming acme's run as its root read acme's attempts: %+v", foreign)
	}
}
