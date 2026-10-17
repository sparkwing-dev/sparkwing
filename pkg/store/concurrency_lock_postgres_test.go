package store_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestPostgresConcurrencyLocksAuthorityBeforeWork(t *testing.T) {
	f := slotFixtureAt(t, storetest.NewPostgres(t))
	f.s.DB().SetMaxOpenConns(8)
	a := slotAuthorization(t, f, "/acquire", "slot")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	blocker, err := f.s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err := blocker.ExecContext(ctx, `SELECT prefix FROM capabilities WHERE prefix=$1 FOR UPDATE`, a.Principal.Authority.ID); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := f.tenant.AcquireAuthorizedSlot(ctx, a, slotRequest(f, "slot")); done <- err }()
	for blocked := 0; blocked == 0; {
		select {
		case err := <-done:
			t.Fatalf("slot did not wait for authority: %v", err)
		default:
		}
		if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	for _, query := range []string{
		`SELECT id FROM runs WHERE team=$1 AND id=$2 FOR UPDATE NOWAIT`,
		`SELECT node_id FROM nodes WHERE team=$1 AND run_id=$2 FOR UPDATE NOWAIT`,
	} {
		if _, err := blocker.ExecContext(ctx, query, f.tenant.Team(), f.ref.RunID); err != nil {
			t.Fatalf("slot locked work before authority: %v", err)
		}
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPostgresConcurrencyReadsDoNotTakeWorkWriteLocks(t *testing.T) {
	f := slotFixtureAt(t, storetest.NewPostgres(t))
	f.s.DB().SetMaxOpenConns(8)
	if _, err := f.tenant.AcquireAuthorizedSlot(t.Context(), slotAuthorization(t, f, "/acquire", "slot"), slotRequest(f, "slot")); err != nil {
		t.Fatal(err)
	}
	holder := slotAuthorization(t, f, "/holder", "slot")
	group := slotAuthorization(t, f, "", "")
	blocker, err := f.s.DB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	for _, query := range []string{
		`SELECT id FROM runs WHERE team=$1 AND id=$2 FOR UPDATE`,
		`SELECT node_id FROM nodes WHERE team=$1 AND run_id=$2 FOR UPDATE`,
	} {
		if _, err := blocker.ExecContext(t.Context(), query, f.tenant.Team(), f.ref.RunID); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := f.tenant.ReadAuthorizedHolder(ctx, holder, "global:deploy", "slot"); err != nil {
		t.Fatalf("holder read waited for work locks: %v", err)
	}
	if _, err := f.tenant.ReadAuthorizedGroup(ctx, group, "global:deploy"); err != nil {
		t.Fatalf("group read waited for work locks: %v", err)
	}
}
