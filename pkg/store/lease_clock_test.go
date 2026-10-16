package store_test

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestSingletonRenewalRefusesMissingFence(t *testing.T) {
	s := storetest.NewSQLite(t).Open(t)
	_, err := s.AsOperator().RenewSingletonLease(t.Context(), store.SingletonLease{Name: "controller.owner", Holder: "missing", Generation: 1}, time.Minute)
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("missing fence refusal: %v", err)
	}
}

func TestPostgresOwnerReleaseWaitsForSharedFence(t *testing.T) {
	s := storetest.NewPostgres(t).Open(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	lease, err := s.AsOperator().AcquireSingletonLease(ctx, "controller.owner", "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Rollback() }()
	if _, err := reader.ExecContext(ctx, `SELECT holder FROM leases WHERE name='controller.owner' FOR KEY SHARE`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := reader.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.AsOperator().ReleaseSingletonLease(ctx, lease) }()
	for blocked := 0; blocked == 0; {
		select {
		case err := <-done:
			t.Fatalf("owner released through a held fence: %v", err)
		default:
		}
		if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPostgresLeaseUsesDatabaseTimeDespiteReplicaSkew(t *testing.T) {
	s := storetest.NewPostgres(t).Open(t)
	var before int64
	if err := s.DB().QueryRow(`SELECT (extract(epoch FROM clock_timestamp())*1000000000)::bigint`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	s.TestOnlyClock(time.Unix(1, 0))
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if lease.ExpiresAt.Before(time.Unix(0, before).Add(time.Minute)) {
		t.Fatalf("replica clock set lease expiry: %v", lease.ExpiresAt)
	}
	s.TestOnlyClock(time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC))
	if _, err := s.AsOperator().RenewSingletonLease(t.Context(), lease, time.Minute); err != nil {
		t.Fatalf("skewed replica refused a live lease: %v", err)
	}
}
