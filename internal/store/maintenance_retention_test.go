package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRewriteRetentionKeepsPinsEffectsAndFinancialRows(t *testing.T) {
	s, _, now := maintenanceStore(t)
	ctx := context.Background()
	old := now.Add(-120 * 24 * time.Hour).UnixNano()
	for i, id := range []string{"old_failure", "old_success", "pinned", "undelivered", "fresh"} {
		state := RunSuccess
		if id == "old_failure" {
			state = RunFailed
		}
		maintenanceRun(t, s, id, i+1, state)
		if id != "fresh" {
			rewriteSchemaExec(t, s.db, `UPDATE runs SET finished_at = ?, event_bytes = 10 WHERE team = 'a' AND id = ?`, old, id)
		}
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO pipeline_success_pins(team,pipeline,run_id,finished_at) VALUES ('a','pipeline','pinned',?)`, old)
	rewriteSchemaExec(t, s.db, `INSERT INTO run_effects(team,run_id,effect,desired_state,desired_rev,next_at) VALUES ('a','undelivered','github_check','success',1,?)`, old)
	rewriteSchemaExec(t, s.db, `INSERT INTO run_effects(team,run_id,effect,desired_state,desired_rev,delivered_rev,next_at,delivered_at) VALUES ('a','pinned','metrics','success',1,1,?,?)`, old, old)
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','pinned','job','pending',?,?)`, []byte(`{}`), []byte(`{}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO node_metrics(team,run_id,node_id,ts,cpu_millicores,memory_bytes) VALUES ('a','pinned','job',?,1,1)`, old)
	rewriteSchemaExec(t, s.db, `INSERT INTO settings(key,value) VALUES ('operator.run_retention_days',?),('operator.node_metric_retention_days',?)`, []byte(`90`), []byte(`7`))
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(team,id,kind,amount_micro,created_at) VALUES ('a','grant','paid',100,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_charges(team,id,run_id,node_id,capability_prefix,seconds,amount_micro,charged_at) VALUES ('a','charge','old_failure','job','cap',1,30,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO storage_run_usage(team,run_id,period,replica_id,bytes,updated_at) VALUES ('a','old_failure','month','replica',123,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO github_app_deliveries(digest,delivery_id,received_at) VALUES ('expired','old',?),('fresh','new',?)`, old, now.UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO github_app_deliveries(digest,delivery_id,received_at,event,body,state,available_at) VALUES ('pending','pending',?,'push',?,'pending',?)`, old, []byte(`{}`), old)
	for _, runID := range []string{"pinned", "fresh"} {
		rewriteSchemaExec(t, s.db, `INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,10,?)`, runID, old)
		rewriteSchemaExec(t, s.db, `INSERT INTO blob_records(team,kind,scope,name,digest,run_id,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,verified_ref,verified_default_branch,ref_facts_json,created_at,expires_at)
VALUES ('a','output',?,'out',?,?,'gh:1','main','principal','cloud',?,'publication',?,0,0,?,?,?)`, runID, runID, runID, []byte(`{}`), runID, []byte(`{}`), old, old)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO capabilities(digest,prefix,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,reserved_pools,created_at,expires_at,created_by)
VALUES ('digest','expired','a','account','one','controller',?,'delegate',1,?,'none',?,1,?,'one')`, []byte(`[]`), []byte(`{}`), []byte(`[]`), now.Add(-8*24*time.Hour).UnixNano())
	lease, err := s.AsOperator().AcquireSingletonLease(ctx, "maintenance", "worker", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.AsOperator().MaintainRetention(ctx, RetentionRequest{Lease: lease, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if result.Runs != 2 || result.Metrics != 1 || result.Records != 1 || result.Capabilities != 1 || result.Effects != 1 || result.PinnedRuns != 1 || result.PinnedOutputs != 1 || result.ReleasedBytes != 20 {
		t.Fatalf("retention: %+v", result)
	}
	for table, want := range map[string]int{"runs": 3, "blob_records": 1, "credit_charges": 1, "storage_run_usage": 1, "run_effects": 1, "github_app_deliveries": 2} {
		rewriteSchemaCount(t, s.db, table, want)
	}
	var balance int64
	if err := s.db.QueryRow(`SELECT (SELECT sum(amount_micro) FROM credit_grants) - (SELECT sum(amount_micro) FROM credit_charges)`).Scan(&balance); err != nil || balance != 70 {
		t.Fatalf("balance = %d: %v", balance, err)
	}
	*now = now.Add(time.Hour)
	if _, err := s.AsOperator().MaintainRetention(ctx, RetentionRequest{Lease: lease, Limit: 100}); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expired retention lease: %v", err)
	}
}

func TestRewriteRetentionDefaultLeavesSelfHostedRuns(t *testing.T) {
	s, _, now := maintenanceStore(t)
	maintenanceRun(t, s, "old", 1, RunFailed)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET finished_at = ? WHERE team = 'a' AND id = 'old'`, now.Add(-365*24*time.Hour).UnixNano())
	lease, err := s.AsOperator().AcquireSingletonLease(context.Background(), "maintenance", "worker", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.AsOperator().MaintainRetention(context.Background(), RetentionRequest{Lease: lease, Limit: 10})
	if err != nil || result.Runs != 0 {
		t.Fatalf("default retention: %+v %v", result, err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO settings(key,value) VALUES ('operator.run_retention_days',?)`, []byte(`"ninety"`))
	if _, err := s.AsOperator().MaintainRetention(context.Background(), RetentionRequest{Lease: lease, Limit: 10}); err == nil {
		t.Fatal("invalid setting succeeded")
	}
	rewriteSchemaCount(t, s.db, "runs", 1)
}
