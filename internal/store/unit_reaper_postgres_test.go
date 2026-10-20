package store_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestPostgresUnitReaperLocksRunBeforeNode(t *testing.T) {
	st := storetest.NewPostgres(t).Open(t)
	tenantFor(t, st, "acme")
	seedExpiredUnitNode(t, st, "acme", "run", 1, 0)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	lease, err := st.AsOperator().AcquireSingletonLease(ctx, "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	db := st.DB()
	report, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = report.Rollback() }()
	if _, err := report.ExecContext(ctx, `SELECT id FROM runs WHERE team='acme' AND id='run' FOR NO KEY UPDATE`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := report.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var reapErr error
	go func() {
		_, reapErr = st.AsOperator().ReapUnits(ctx, lease, 1)
		close(done)
	}()
	defer func() { cancel(); _ = report.Rollback(); <-done }()
	for blocked := 0; blocked == 0; {
		select {
		case <-done:
			t.Fatalf("reaper finished while the run was held: %v", reapErr)
		default:
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	if _, err := report.ExecContext(ctx, `SELECT 1 FROM nodes WHERE team='acme' AND run_id='run' AND node_id='job' FOR UPDATE NOWAIT`); err != nil {
		t.Fatalf("reaper holds node while waiting for run: %v", err)
	}
	if err := report.Rollback(); err != nil {
		t.Fatal(err)
	}
	<-done
	if reapErr != nil {
		t.Fatal(reapErr)
	}
}
