package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestUnitReaperUsesTenantOwnerLease(t *testing.T) {
	st := storetest.Open(t)
	tenant := tenantFor(t, st, "acme")
	now := time.Now()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := st.DB().ExecContext(t.Context(), storetest.Rebind(st, query), args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,
repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days)
VALUES ('acme','local',1,'ci','running',?,'request',1,'local:repo','host','execute',?,'account',?,'r30',30)`, []byte(`{"request":{"origin":"local"}}`), []byte(`[]`), now.Add(-time.Hour).UnixNano())
	exec(`INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,started_at,needs_labels,prefers_labels)
VALUES ('acme','local','claimed','trigger',1,1,'host','owner',1,?,?,?,?)`, now.Add(-time.Minute).UnixNano(), now.Add(-time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`))
	exec(`INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json)
VALUES ('acme','local','job','pending',?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	lease, err := st.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reaped, err := st.ReapUnits(t.Context(), lease, 10)
	if err != nil || len(reaped) != 1 || reaped[0].Team != "acme" || !reaped[0].Failed {
		t.Fatalf("reaped owners: %+v, %v", reaped, err)
	}
	run, err := tenant.ReadRun(t.Context(), "local")
	if err != nil || run.State != store.RunFailed || run.FinishedAt == nil {
		t.Fatalf("local owner: %+v, %v", run, err)
	}
	job, err := tenant.ReadNode(t.Context(), store.NodeRef{RunID: "local", NodeID: "job"})
	if err != nil || job.Outcome != store.NodeCancelled || job.StateSeq != 1 {
		t.Fatalf("unstarted job: %+v, %v", job, err)
	}
}
