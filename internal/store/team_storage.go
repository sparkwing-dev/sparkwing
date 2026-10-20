package store

import (
	"context"
	"math/rand/v2"
	"time"
)

// StorageKind names the storage inventory class.
type StorageKind string

const (
	// StorageCache is the cache's binaries, dependency archives and
	// artifacts.
	StorageCache StorageKind = "cache"
	// StorageLogs is the node log segments.
	StorageLogs StorageKind = "logs"
)

// Valid reports whether k names a store the controller counts.
func (k StorageKind) Valid() bool { return k == StorageCache || k == StorageLogs }

// FreeCacheShare is the part of allowance the cache holds a free team to,
// three quarters.
func FreeCacheShare(allowance int64) int64 { return allowance / 4 * 3 }

// FreeLogShare is the part of allowance a free team's node logs are held
// to, three sixteenths.
func FreeLogShare(allowance int64) int64 { return allowance / 16 * 3 }

func execStatements(ctx context.Context, tx *storeTx, script string) error {
	statements, err := splitStatements(script)
	if err != nil {
		return err
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// safety: a PostgreSQL deadlock or serialization failure aborts only the victim, so inTx
// reruns fn in a fresh transaction; fn resets any state it shares with its caller.
func (s *Store) inTx(ctx context.Context, fn func(*storeTx) error) error {
	return retryConflict(ctx, func() error { return s.inTxOnce(ctx, fn) }, sleepCtx)
}

func retryConflict(ctx context.Context, attempt func() error, sleep func(context.Context, time.Duration) error) error {
	const attempts, base, ceiling = 5, 10 * time.Millisecond, 250 * time.Millisecond
	var err error
	for i := range attempts {
		if err = attempt(); err == nil || !isPGConflict(err) || i == attempts-1 {
			return err
		}
		if sleep(ctx, rand.N(min(base<<i, ceiling))) != nil {
			return err
		}
	}
	return err
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Store) inTxOnce(ctx context.Context, fn func(*storeTx) error) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
