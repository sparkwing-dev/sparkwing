package store

import (
	"context"
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
	for _, stmt := range splitStatements(script) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// safety: the lock order puts the free tier before team_storage rows, so a
// commit takes its slot before it locks a row. A commit whose team finds every
// slot taken is refused, so no team without a slot commits a byte.
func takeStorageSlotTx(ctx context.Context, tx *storeTx, team Team, bytes int64, now time.Time) error {
	team = NormalizeTeam(team)
	if bytes <= 0 || !holdsFreeAllowance(team) {
		return nil
	}
	standing, err := storageStandingTx(ctx, tx, team)
	if err != nil || standing.Tier != TeamTierNone {
		return err
	}
	if now.IsZero() {
		now = time.Now()
	}
	if err := lockFreeTierTx(ctx, tx); err != nil {
		return err
	}
	slotted, err := takeFreeSlotTx(ctx, tx, team, now)
	if err != nil || slotted {
		return err
	}
	return freeStoragePaused(team)
}

// safety: a refusal returns before the first write, so a caller that keeps
// the transaction after one commits nothing of the reservation.
// safety: every change to a team_storage row locks it here first, so the lock orders them.
func lockTeamStorageTx(ctx context.Context, tx *storeTx, team Team, kind StorageKind, now time.Time) (used, reserved int64, err error) {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO team_storage (team, store, used_bytes, reserved_bytes, committed_bytes, reconciled_at, updated_at)
VALUES (?, ?, 0, 0, 0, 0, ?) ON CONFLICT (team, store) DO NOTHING`,
		string(team), string(kind), now.UnixNano()); err != nil {
		return 0, 0, err
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT used_bytes, reserved_bytes FROM team_storage WHERE team = ? AND store = ?`+tx.forUpdate(),
		string(team), string(kind)).Scan(&used, &reserved); err != nil {
		return 0, 0, err
	}
	return used, reserved, nil
}

func addLegacyLogStorageTx(ctx context.Context, tx *storeTx, team Team, bytes int64, now time.Time) error {
	if _, _, err := lockTeamStorageTx(ctx, tx, team, StorageLogs, now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE team_storage SET used_bytes = used_bytes + ?, committed_bytes = committed_bytes + ?, updated_at = ? WHERE team = ? AND store = ?`, bytes, bytes, now.UnixNano(), team, StorageLogs)
	return err
}

func (s *Store) inTx(ctx context.Context, fn func(*storeTx) error) (err error) {
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
