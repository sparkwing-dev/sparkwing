package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// ReapUnits recovers expired work under the elected reaper fence.
func (s *Store) ReapUnits(ctx context.Context, lease SingletonLease, limit int) ([]ReapedUnit, error) {
	if limit <= 0 {
		return nil, errors.New("reaper limit must be positive")
	}
	var out []ReapedUnit
	err := s.inTx(ctx, func(tx *storeTx) error {
		now := tx.now()
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT holder FROM leases WHERE name='controller.owner'`+tx.forUpdate()).Scan(&owner); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := assertSingletonLeaseTx(ctx, tx, lease, "controller.reaper", now); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT team,run_id,node_id FROM nodes WHERE claimed_by IS NOT NULL AND lease_expires_at<=? AND status<>'done' ORDER BY lease_expires_at,team,run_id,node_id LIMIT ?`, now.UnixNano(), limit)
		if err != nil {
			return err
		}
		type target struct {
			team Team
			ref  NodeRef
		}
		var targets []target
		for rows.Next() {
			var v target
			if err = rows.Scan(&v.team, &v.ref.RunID, &v.ref.NodeID); err != nil {
				_ = rows.Close()
				return err
			}
			targets = append(targets, v)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		actor := Actor{Kind: ActorReaper, Owner: OwnerLeaseRef{Kind: OwnerController, Name: lease.Name, Holder: lease.Holder, Generation: lease.Generation}}
		for _, target := range targets {
			row, err := readNodeTx(ctx, tx, target.team, target.ref)
			if err != nil {
				return err
			}
			run, err := readRunTx(ctx, tx, target.team, target.ref.RunID)
			if err != nil {
				return err
			}
			to := NodePending
			if row.CancelRequestedAt != nil || run.CancelRequestedAt != nil {
				to = NodeCancelled
			} else if row.Status == StatusRunning && row.Disruptions+1 >= limits.MaxDisruptions {
				to = NodeFailed
			}
			if _, err = transitionNodeTx(ctx, tx, target.team, NodeTransition{Ref: target.ref, To: to, Actor: actor, Cause: CauseLeaseLapse}, now); err != nil {
				return err
			}
			disruptions := row.Disruptions
			if row.Status == StatusRunning && to != NodeCancelled {
				disruptions++
			}
			out = append(out, ReapedUnit{Team: target.team, RunID: target.ref.RunID, NodeID: target.ref.NodeID, Kind: "node", Generation: row.Generation, Disruptions: disruptions, Requeued: to == NodePending, Cancelled: to == NodeCancelled, Failed: to == NodeFailed})
		}
		return nil
	})
	return out, err
}
