package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CapacityBinding makes capacity follow work across owner loss instead of following a process.
type CapacityBinding struct {
	RunID      string
	NodeID     string
	Generation int64
}

// CapacityHolder retains its own lease as proof that capacity has counted continuously.
type CapacityHolder struct {
	Key            string
	ID             string
	Binding        CapacityBinding
	Cost           int
	AcquiredAt     time.Time
	LeaseExpiresAt time.Time
	Superseded     bool
}

// CapacityWaiter keeps its arrival order only while its own lease remains live.
type CapacityWaiter struct {
	Key            string
	ID             string
	Binding        CapacityBinding
	ArrivedAt      time.Time
	LeaseExpiresAt time.Time
}

// AdoptionKind refuses to revive expired capacity; a new arrival goes through normal admission.
type AdoptionKind string

// Adoption outcomes preserve continuously retained holders and waiters only.
const (
	AdoptHolder  AdoptionKind = "holder"
	AdoptWaiter  AdoptionKind = "waiter"
	AcquireAgain AdoptionKind = "new_arrival"
)

// CapacityAdoption reports the store's liveness decision, never a caller-supplied original arrival time.
type CapacityAdoption struct {
	Kind   AdoptionKind
	Holder *CapacityHolder
	Waiter *CapacityWaiter
}

// AdoptCapacity checks continuity under the entry lock; expired holders and waiters are removed before a fresh request.
func (t *Tenant) AdoptCapacity(ctx context.Context, key, id string, binding CapacityBinding, actor Actor) (CapacityAdoption, error) {
	if key == "" || id == "" || binding.RunID == "" || binding.Generation < 0 {
		return CapacityAdoption{}, fmt.Errorf("invalid capacity binding")
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return CapacityAdoption{}, err
	}
	defer rollbackOrLog(tx)
	fence := capacityFence{binding: binding, actor: actor}
	if err := checkCapacityFenceTx(ctx, tx, t.team, fence); err != nil {
		return CapacityAdoption{}, err
	}
	if err := lockCapacityEntryTx(ctx, tx, t.team, key); err != nil {
		return CapacityAdoption{}, err
	}
	if err := checkCapacityFenceTx(ctx, tx, t.team, fence); err != nil {
		return CapacityAdoption{}, err
	}
	adopted, err := adoptCapacityTx(ctx, tx, t.team, key, id, binding)
	if err != nil {
		return CapacityAdoption{}, err
	}
	if err := tx.Commit(); err != nil {
		return CapacityAdoption{}, err
	}
	return adopted, nil
}

func lockCapacityEntryTx(ctx context.Context, tx *storeTx, team Team, key string) error {
	var found string
	err := tx.QueryRowContext(ctx, `SELECT key FROM concurrency_entries WHERE team = ? AND key = ?`+tx.forUpdate(), team, key).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func adoptCapacityTx(ctx context.Context, tx *storeTx, team Team, key, id string, binding CapacityBinding) (CapacityAdoption, error) {
	now := tx.now(ctx).UnixNano()
	var holder CapacityHolder
	var acquired, expires int64
	err := tx.QueryRowContext(ctx, `SELECT run_id,node_id,generation,cost,claimed_at,lease_expires_at,superseded FROM concurrency_holders WHERE team = ? AND key = ? AND holder_id = ?`, team, key, id).Scan(&holder.Binding.RunID, &holder.Binding.NodeID, &holder.Binding.Generation, &holder.Cost, &acquired, &expires, &holder.Superseded)
	if err == nil {
		if holder.Binding.RunID != binding.RunID || holder.Binding.NodeID != binding.NodeID {
			return CapacityAdoption{}, holderRefusal()
		}
		if expires > now {
			if holder.Superseded {
				return CapacityAdoption{}, ErrConcurrencySuperseded
			}
			if _, err := tx.ExecContext(ctx, `UPDATE concurrency_holders SET generation = ? WHERE team = ? AND key = ? AND holder_id = ?`, binding.Generation, team, key, id); err != nil {
				return CapacityAdoption{}, err
			}
			holder.Key, holder.ID, holder.Binding = key, id, binding
			holder.AcquiredAt, holder.LeaseExpiresAt = time.Unix(0, acquired), time.Unix(0, expires)
			return CapacityAdoption{Kind: AdoptHolder, Holder: &holder}, nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM concurrency_holders WHERE team = ? AND key = ? AND holder_id = ?`, team, key, id); err != nil {
			return CapacityAdoption{}, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return CapacityAdoption{}, err
	}
	var waiter CapacityWaiter
	err = tx.QueryRowContext(ctx, `SELECT run_id,node_id,generation,arrived_at,lease_expires_at FROM concurrency_waiters WHERE team = ? AND key = ? AND holder_id = ? AND run_id = ? AND node_id = ?`, team, key, id, binding.RunID, binding.NodeID).Scan(&waiter.Binding.RunID, &waiter.Binding.NodeID, &waiter.Binding.Generation, &acquired, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return CapacityAdoption{Kind: AcquireAgain}, nil
	}
	if err != nil {
		return CapacityAdoption{}, err
	}
	if expires <= now {
		_, err := tx.ExecContext(ctx, `DELETE FROM concurrency_waiters WHERE team = ? AND key = ? AND run_id = ? AND node_id = ?`, team, key, binding.RunID, binding.NodeID)
		return CapacityAdoption{Kind: AcquireAgain}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE concurrency_waiters SET generation = ? WHERE team = ? AND key = ? AND run_id = ? AND node_id = ?`, binding.Generation, team, key, binding.RunID, binding.NodeID); err != nil {
		return CapacityAdoption{}, err
	}
	waiter.Key, waiter.ID, waiter.Binding = key, id, binding
	waiter.ArrivedAt, waiter.LeaseExpiresAt = time.Unix(0, acquired), time.Unix(0, expires)
	return CapacityAdoption{Kind: AdoptWaiter, Waiter: &waiter}, nil
}
