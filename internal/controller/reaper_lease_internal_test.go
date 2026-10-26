package controller

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestReaperRecoversAnExpiredJobClaimOnce(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if _, err := teststore.SQL(t, st).ExecContext(ctx, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) VALUES (?,'run',1,'p','running',?,'request',1,'gh:1','host','execute','account',1,'r30',30)`, store.DefaultTeam, []byte(`{"version":1,"provenance":{"trust":"asserted","pipeline":"p","writer":"account/fixture"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, st).ExecContext(ctx, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,claimed_by,capability_prefix,generation,attempt,started_at,lease_expires_at) VALUES (?,'run','job','running',?,?,'runner','claim',2,3,1,1)`, store.DefaultTeam, []byte(`{"id":"job"}`), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, st).ExecContext(ctx, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES (?,'run','job',3,2,'runner','claim','runner','runner','remote',1)`, store.DefaultTeam); err != nil {
		t.Fatal(err)
	}
	srv := New(st, slog.New(slog.DiscardHandler))
	for range 2 {
		srv.reap(ctx)
	}
	tenant, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadNode(ctx, runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || row.Status != runstate.StatusPending || row.Disruptions != 1 || row.Generation != 2 {
		t.Fatalf("recovered job = %+v, %v", row, err)
	}
	run, err := tenant.ReadRun(ctx, "run")
	if err != nil || run.State != runstate.RunRunning {
		t.Fatalf("recovered run = %+v, %v", run, err)
	}
	events, err := tenant.ReadStateEvents(ctx, "run", 0, 10)
	if err != nil || len(events) != 1 || events[0].Kind != "job_requeued" {
		t.Fatalf("recovery events = %+v, %v", events, err)
	}
}
