package store

import (
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestRetentionUsesFinishTimeForLongRunningWork(t *testing.T) {
	s, tenant, now := maintenanceStore(t)
	maintenanceRun(t, s, "recent", 1, runstate.RunSuccess)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET started_at=? WHERE id='recent'`, now.Add(-120*24*time.Hour).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO settings(key,value) VALUES ('operator.run_retention_days',?)`, []byte(`90`))
	result, err := s.AsOperator().MaintainRetention(t.Context(), RetentionRequest{Limit: 10})
	if err != nil || result.Runs != 0 {
		t.Fatalf("recent finish pruned: %+v err=%v", result, err)
	}
	if run, err := tenant.ReadRun(t.Context(), "recent"); err != nil || run.FinishedAt == nil || !run.FinishedAt.Equal(*now) {
		t.Fatalf("recent run: %+v err=%v", run, err)
	}
}

func TestRetentionRollsBackAllRunDeletesAfterLaterFailure(t *testing.T) {
	s, tenant, now := maintenanceStore(t)
	for i, id := range []string{"first", "second"} {
		maintenanceRun(t, s, id, i+1, runstate.RunFailed)
		rewriteSchemaExec(t, s.db, `UPDATE runs SET finished_at=?,event_bytes=17 WHERE id=?`, now.Add(-120*24*time.Hour).UnixNano(), id)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO settings(key,value) VALUES ('operator.run_retention_days',?)`, []byte(`90`))
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER deny_second_delete BEFORE DELETE ON runs WHEN OLD.id='second' BEGIN SELECT RAISE(ABORT,'retained'); END`)
	result, err := s.AsOperator().MaintainRetention(t.Context(), RetentionRequest{Limit: 10})
	if err == nil || !reflect.DeepEqual(result, RetentionResult{}) {
		t.Fatalf("failed pass reported committed deletions: %+v err=%v", result, err)
	}
	for _, id := range []string{"first", "second"} {
		if _, err := tenant.ReadRun(t.Context(), id); err != nil {
			t.Fatalf("failed pass lost %s: %v", id, err)
		}
	}
	rewriteSchemaExec(t, s.db, `DROP TRIGGER deny_second_delete`)
	result, err = s.AsOperator().MaintainRetention(t.Context(), RetentionRequest{Limit: 10})
	if err != nil || result.Runs != 2 || result.ReleasedBytes != 34 {
		t.Fatalf("successful pass: %+v err=%v", result, err)
	}
	rewriteSchemaCount(t, s.db, "runs", 0)
}
