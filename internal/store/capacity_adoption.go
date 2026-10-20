package store

import (
	"context"
	"database/sql"
	"errors"
)

// CapacityBinding makes capacity follow work across owner loss instead of following a process.
type CapacityBinding struct {
	RunID      string
	NodeID     string
	Generation int64
}

// adoptionKind refuses to revive expired capacity; a new arrival goes through normal admission.
type adoptionKind string

// Adoption outcomes preserve continuously retained holders and waiters only.
const (
	adoptHolder  adoptionKind = "holder"
	adoptWaiter  adoptionKind = "waiter"
	acquireAgain adoptionKind = "new_arrival"
)

func adoptCapacityTx(ctx context.Context, tx *storeTx, team Team, key, id string, binding CapacityBinding) (adoptionKind, error) {
	now := tx.now().UnixNano()
	var current CapacityBinding
	var expires int64
	var superseded bool
	err := tx.QueryRowContext(ctx, `SELECT run_id,node_id,lease_expires_at,superseded FROM concurrency_holders WHERE team = ? AND key = ? AND holder_id = ?`, team, key, id).Scan(&current.RunID, &current.NodeID, &expires, &superseded)
	if err == nil {
		if current.RunID != binding.RunID || current.NodeID != binding.NodeID {
			return "", holderRefusal()
		}
		if expires > now {
			if superseded {
				return "", ErrConcurrencySuperseded
			}
			if _, err := tx.ExecContext(ctx, `UPDATE concurrency_holders SET generation = ? WHERE team = ? AND key = ? AND holder_id = ?`, binding.Generation, team, key, id); err != nil {
				return "", err
			}
			return adoptHolder, nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM concurrency_holders WHERE team = ? AND key = ? AND holder_id = ?`, team, key, id); err != nil {
			return "", err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	err = tx.QueryRowContext(ctx, `SELECT lease_expires_at FROM concurrency_waiters WHERE team = ? AND key = ? AND holder_id = ? AND run_id = ? AND node_id = ?`, team, key, id, binding.RunID, binding.NodeID).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return acquireAgain, nil
	}
	if err != nil {
		return "", err
	}
	if expires <= now {
		_, err := tx.ExecContext(ctx, `DELETE FROM concurrency_waiters WHERE team = ? AND key = ? AND run_id = ? AND node_id = ?`, team, key, binding.RunID, binding.NodeID)
		return acquireAgain, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE concurrency_waiters SET generation = ? WHERE team = ? AND key = ? AND run_id = ? AND node_id = ?`, binding.Generation, team, key, binding.RunID, binding.NodeID); err != nil {
		return "", err
	}
	return adoptWaiter, nil
}

func lockCapacityEntryTx(ctx context.Context, tx *storeTx, team Team, key string) error {
	var found string
	err := tx.QueryRowContext(ctx, `SELECT key FROM concurrency_entries WHERE team = ? AND key = ?`+tx.forUpdate(), team, key).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
