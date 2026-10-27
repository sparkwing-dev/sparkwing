package store

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// ControllerRun identifies a visited run and carries its tenant into the next operation.
type ControllerRun struct {
	Team  Team
	RunID string
}

// controllerOwnedHorizon bounds the capacity and owner deadlines of controller-owned runs. The controller renews them
// on every reconciliation pass, so they lapse only while no controller runs.
const controllerOwnedHorizon = time.Minute

// controllerActor is the actor for the controller's own transitions on runID; the single active controller (the
// store's advisory lock and write epoch) is its authority, so it carries no fence.
func controllerActor(runID string) runstate.Actor {
	return runstate.Actor{Kind: runstate.ActorController, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController, EvalFence: runspec.EvalFence{RunID: runspec.RunID(runID)}}}
}

const controllerRunOwnership = `r.dispatch='controller' AND r.plan_accepted_generation>0`

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

// ReconcileControllerRuns resumes settlement of controller-owned runs and renews their capacity.
// Pages use the last returned RunID as after; an empty page ends the sweep.
// Each visited run commits separately, so an error returns the committed prefix.
// The next sweep starts with an empty cursor to revisit runs still awaiting work.
func (o *Operator) ReconcileControllerRuns(ctx context.Context, after string, limit int) ([]ControllerRun, error) {
	if limit <= 0 {
		return nil, errors.New("controller reconciliation limit must be positive")
	}
	var targets []ControllerRun
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		targets = targets[:0]
		rows, err := tx.queryContext(ctx, `SELECT r.team,r.id FROM runs r WHERE `+controllerRunActive+` AND r.id>? ORDER BY r.id LIMIT ?`, after, limit)
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
			actor := controllerActor(ref.RunID)
			if run.State == runstate.RunPending && run.CancelRequestedAt == nil {
				if _, err := transitionRunTx(ctx, tx, ref.Team, runstate.RunTransition{RunID: runspec.RunID(ref.RunID), To: runstate.RunRunning, Actor: actor, Cause: runstate.RunStart}); err != nil {
					return err
				}
			}
			if err := settleControllerTx(ctx, tx, ref.Team, ref.RunID, tx.now(ctx)); err != nil {
				return err
			}
			return renewCapacityTx(ctx, tx, ref.Team, ref.RunID, "", tx.now(ctx).Add(controllerOwnedHorizon))
		})
		if err != nil {
			return visited, err
		}
		visited = append(visited, ref)
	}
	return visited, nil
}
