package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// InTxForTest runs fn through inTx for the external Postgres retry test.
func (s *Store) InTxForTest(ctx context.Context, fn func(*sql.Tx) error) error {
	return s.inTx(ctx, func(tx *storeTx) error { return fn(tx.tx) })
}

func TestRetryConflictBoundsAttemptsAndBackoff(t *testing.T) {
	deadlock := fmt.Errorf("update: %w", &pgconn.PgError{Code: "40P01"})
	var waits []time.Duration
	sleep := func(context.Context, time.Duration) error { return nil }
	record := func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	calls := 0
	if err := retryConflict(t.Context(), func() error { calls++; return deadlock }, record); err != deadlock {
		t.Fatalf("err = %v, want last deadlock", err)
	}
	if calls != 5 || len(waits) != 4 {
		t.Fatalf("calls = %d waits = %d, want 5 attempts and 4 waits", calls, len(waits))
	}
	for i, d := range waits {
		if limit := min(10*time.Millisecond<<i, 250*time.Millisecond); d < 0 || d >= limit {
			t.Fatalf("wait %d = %v, want [0,%v)", i, d, limit)
		}
	}

	calls = 0
	serialization := &pgconn.PgError{Code: "40001"}
	if err := retryConflict(t.Context(), func() error {
		calls++
		if calls == 1 {
			return serialization
		}
		return nil
	}, sleep); err != nil || calls != 2 {
		t.Fatalf("err = %v calls = %d, want success on second attempt", err, calls)
	}

	for _, other := range []error{&pgconn.PgError{Code: "23505"}, errors.New("database is locked")} {
		calls = 0
		if err := retryConflict(t.Context(), func() error { calls++; return other }, sleep); err != other || calls != 1 {
			t.Fatalf("err = %v calls = %d, want %v returned without retry", err, calls, other)
		}
	}

	calls = 0
	cancelled := func(context.Context, time.Duration) error { return context.Canceled }
	if err := retryConflict(t.Context(), func() error { calls++; return deadlock }, cancelled); err != deadlock || calls != 1 {
		t.Fatalf("err = %v calls = %d, want the conflict back once the context ends", err, calls)
	}
}

func TestBusyRetryTreatsPostgresConflictsAsBusy(t *testing.T) {
	calls := 0
	err := retryOnBusyWithSleep(func() error {
		calls++
		if calls < 3 {
			return &pgconn.PgError{Code: "40001"}
		}
		return nil
	}, func(time.Duration) {})
	if err != nil || calls != 3 {
		t.Fatalf("err = %v calls = %d, want success after two serialization failures", err, calls)
	}
}
