package store

import (
	"context"
	"errors"
	"time"
)

// ControllerRun identifies a visited run and carries its tenant into the next operation.
type ControllerRun struct {
	Team  Team
	RunID string
}

// A stopped metered host cannot settle its accepted graph. The controller uses the same scheduler for that graph.
const controllerRunOwnership = `r.plan_accepted_generation>0 AND
 (r.dispatch='controller' OR (r.dispatch='host' AND r.intake_mode='execute' AND r.sealed_at IS NOT NULL AND EXISTS (
 SELECT 1 FROM triggers t JOIN billing_awards a ON a.team=t.team AND a.run_id=t.id AND a.generation=t.generation AND a.unit_kind=t.unit_kind
 WHERE t.team=r.team AND t.id=r.id AND t.unit_kind='trigger' AND t.status='failed' AND a.metered=1 AND a.stop_reason<>'')))`

const controllerRunScheduling = controllerRunOwnership + ` AND r.intake_mode='execute' AND r.sealed_at IS NOT NULL`

const controllerRunActive = controllerRunScheduling + ` AND r.status IN ('pending','running')`

func controllerOwnershipTx(ctx context.Context, tx *storeTx, team Team, runID string) (bool, error) {
	var owned bool
	err := tx.QueryRowContext(ctx, `SELECT `+controllerRunOwnership+` AND r.intake_mode='execute' FROM runs r WHERE team=? AND id=?`, team, runID).Scan(&owned)
	return owned, err
}

func controllerSchedulingTx(ctx context.Context, tx *storeTx, team Team, runID string) (bool, error) {
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT `+controllerRunScheduling+` FROM runs r WHERE team=? AND id=?`, team, runID).Scan(&active)
	return active, err
}

// ReconcileControllerRuns resumes settlement under the elected controller fence.
// Pages use the last returned RunID as after; an empty page ends the sweep.
// Each visited run commits separately, so an error returns the committed prefix.
// The next sweep starts with an empty cursor to revisit runs still awaiting work.
func (o *Operator) ReconcileControllerRuns(ctx context.Context, lease SingletonLease, after string, limit int) ([]ControllerRun, error) {
	if limit <= 0 {
		return nil, errors.New("controller reconciliation limit must be positive")
	}
	var targets []ControllerRun
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		targets = targets[:0]
		if err := assertSingletonLeaseTx(ctx, tx, lease, "controller.owner", tx.now()); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT r.team,r.id FROM runs r WHERE `+controllerRunActive+` AND r.id>? ORDER BY r.id LIMIT ?`, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ref ControllerRun
			if err := rows.Scan(&ref.Team, &ref.RunID); err != nil {
				return err
			}
			targets = append(targets, ref)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	var visited []ControllerRun
	for _, ref := range targets {
		err := o.s.inTx(ctx, func(tx *storeTx) error {
			if err := assertSingletonLeaseTx(ctx, tx, lease, "controller.owner", tx.now()); err != nil {
				return err
			}
			if err := lockStateRunTx(ctx, tx, ref.Team, ref.RunID); err != nil {
				return err
			}
			var active bool
			if err := tx.QueryRowContext(ctx, `SELECT `+controllerRunActive+` FROM runs r WHERE team=? AND id=?`, ref.Team, ref.RunID).Scan(&active); err != nil {
				return err
			}
			if !active {
				return nil
			}
			run, err := readRunTx(ctx, tx, ref.Team, ref.RunID)
			if err != nil {
				return err
			}
			actor := Actor{Kind: ActorController, Owner: OwnerLeaseRef{Kind: OwnerController, Name: lease.Name, Holder: lease.Holder, Generation: lease.Generation, RunID: ref.RunID}}
			if run.State == RunPending && run.CancelRequestedAt == nil {
				if _, err := transitionRunTx(ctx, tx, ref.Team, RunTransition{RunID: ref.RunID, To: RunRunning, Actor: actor, Cause: RunStart}, tx.now()); err != nil {
					return err
				}
			}
			if err := settleControllerTx(ctx, tx, ref.Team, ref.RunID, tx.now()); err != nil {
				return err
			}
			var until int64
			if err := tx.QueryRowContext(ctx, "SELECT until FROM leases WHERE name='controller.owner'").Scan(&until); err != nil {
				return err
			}
			if err := renewCapacityTx(ctx, tx, ref.Team, ref.RunID, "", tx.now(), time.Unix(0, until)); err != nil {
				return err
			}
			return assertSingletonLeaseTx(ctx, tx, lease, "controller.owner", tx.now())
		})
		if err != nil {
			return visited, err
		}
		visited = append(visited, ref)
	}
	return visited, nil
}
