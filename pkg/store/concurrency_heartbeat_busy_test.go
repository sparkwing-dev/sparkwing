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

// busyProneDSN mirrors Open's DSN but with busy_timeout(0): a write that
// meets a held write lock fails immediately with SQLITE_BUSY instead of
// busy-waiting. It isolates the heartbeat's own retry from the DSN-level
// busy_timeout that would otherwise absorb the contention.
func busyProneDSN(path string) string {
	return fmt.Sprintf("file:%s?_txlock=immediate&_pragma=busy_timeout(0)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(on)", path)
}

// TestHeartbeatConcurrencySlot_RetriesTransientBusy holds the write lock
// on a separate connection, fires a heartbeat against a busy_timeout(0)
// store so the first attempts see an un-absorbed SQLITE_BUSY, then frees
// the lock mid-flight. Success can only come from the heartbeat's own
// bounded retry; without it the heartbeat would lapse a live lease.
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
	if _, err := lockTx.ExecContext(ctx,
		`UPDATE concurrency_holders SET lease_expires_at = lease_expires_at WHERE key = 'k'`,
	); err != nil {
		t.Fatalf("take write lock: %v", err)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = lockTx.Rollback()
		close(released)
	}()

	expires, _, err := hb.HeartbeatConcurrencySlot(ctx, "k", "r1/n1", 30*time.Second)
	if err != nil {
		t.Fatalf("heartbeat under transient busy: %v", err)
	}
	<-released
	if !expires.After(time.Now()) {
		t.Errorf("lease not extended into the future: %v", expires)
	}
}

// TestHeartbeatConcurrencySlot_LostHolderDoesNotRetry confirms a
// non-busy terminal outcome (the holder row is gone) short-circuits:
// ErrLockHeld must surface immediately rather than spin the retry, so an
// expired or reassigned holder is not kept alive by the busy budget.
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
