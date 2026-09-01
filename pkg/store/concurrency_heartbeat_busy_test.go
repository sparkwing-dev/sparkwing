package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func busyProneDSN(path string) string {
	return fmt.Sprintf("file:%s?_txlock=immediate&_pragma=busy_timeout(0)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(on)", path)
}

func TestHeartbeatConcurrencySlot_RetriesTransientBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()

	seed, err := Open(path)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if _, err := seed.AcquireConcurrencySlot(ctx, AcquireSlotRequest{
		Key: "k", HolderID: "r1/n1", RunID: "r1", NodeID: "n1",
		Capacity: 1, Policy: OnLimitQueue,
	}); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	_ = seed.Close()

	hb, err := openSQL("sqlite", busyProneDSN(path), DialectSQLite)
	if err != nil {
		t.Fatalf("busy-prone open: %v", err)
	}
	defer func() { _ = hb.Close() }()

	locker, err := sql.Open("sqlite", busyProneDSN(path))
	if err != nil {
		t.Fatalf("locker open: %v", err)
	}
	locker.SetMaxOpenConns(1)
	defer func() { _ = locker.Close() }()

	lockTx, err := locker.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	defer func() { _ = lockTx.Rollback() }()
	if _, err := lockTx.ExecContext(ctx,
		`UPDATE concurrency_holders SET lease_expires_at = lease_expires_at WHERE key = 'k'`,
	); err != nil {
		t.Fatalf("take write lock: %v", err)
	}

	retries := 0
	releaseOnRetry := func(time.Duration) {
		retries++
		if err := lockTx.Rollback(); err != nil {
			t.Fatalf("release write lock: %v", err)
		}
	}
	started := time.Now()
	expires, _, err := hb.heartbeatConcurrencySlot(ctx, "k", "r1/n1", 30*time.Second, releaseOnRetry)
	if err != nil {
		t.Fatalf("heartbeat under transient busy: %v", err)
	}
	if retries != 1 {
		t.Fatalf("busy retries = %d, want 1", retries)
	}
	if !expires.After(time.Now()) {
		t.Errorf("lease not extended into the future: %v", expires)
	}
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("transient-busy heartbeat took %s, want under 100ms", elapsed)
	}
}

func TestHeartbeatConcurrencySlot_LostHolderDoesNotRetry(t *testing.T) {
	s := openStoreT(t)
	ctx := context.Background()

	start := time.Now()
	_, _, err := s.HeartbeatConcurrencySlot(ctx, "missing", "h", 10*time.Second)
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("err = %v, want ErrLockHeld", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("lost-holder heartbeat took %v; should not have retried", elapsed)
	}
}

func openStoreT(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
