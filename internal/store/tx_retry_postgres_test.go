package store_test

import (
	"context"
	"database/sql"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestPostgresInTxRetriesDeadlockVictim(t *testing.T) {
	st := storetest.OpenPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	db := st.DB()
	if _, err := db.ExecContext(ctx, `CREATE TABLE retry_probe (id int PRIMARY KEY, by_store int NOT NULL DEFAULT 0, by_blocker int NOT NULL DEFAULT 0);
INSERT INTO retry_probe (id) VALUES (1), (2)`); err != nil {
		t.Fatal(err)
	}
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	var super bool
	if err := blocker.QueryRowContext(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname=current_user`).Scan(&super); err != nil {
		t.Fatal(err)
	}
	if super {
		// safety: the store's wait then always reaches deadlock detection first.
		if _, err := blocker.ExecContext(ctx, `SET LOCAL deadlock_timeout = '20s'`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := blocker.ExecContext(ctx, `UPDATE retry_probe SET by_blocker=1 WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}

	attempts := 0
	var firstErr error
	done := make(chan error, 1)
	go func() {
		done <- st.InTxForTest(ctx, func(tx *sql.Tx) error {
			attempts++
			if _, err := tx.ExecContext(ctx, `UPDATE retry_probe SET by_store=1 WHERE id=1`); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE retry_probe SET by_store=1 WHERE id=2`)
			if attempts == 1 {
				firstErr = err
			}
			return err
		})
	}()
	for blocked := 0; blocked == 0; {
		select {
		case err := <-done:
			t.Fatalf("store transaction finished before the blocker released row 2: %v", err)
		default:
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	if _, err := blocker.ExecContext(ctx, `UPDATE retry_probe SET by_blocker=1 WHERE id=1`); err != nil {
		t.Fatalf("blocker was the deadlock victim: %v", err)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("inTx = %v, want the retried attempt to commit", err)
	}
	var pgErr *pgconn.PgError
	if !errors.As(firstErr, &pgErr) || pgErr.Code != "40P01" {
		t.Fatalf("first attempt error = %v, want deadlock 40P01", firstErr)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	var written int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM retry_probe WHERE by_store=1 AND by_blocker=1`).Scan(&written); err != nil {
		t.Fatal(err)
	}
	if written != 2 {
		t.Fatalf("rows carrying both writes = %d, want 2", written)
	}
}
