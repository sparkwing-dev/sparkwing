package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func transitionNodeTx(ctx context.Context, tx *storeTx, team Team, change nodeTransition, now time.Time) (Applied, error) {
	if _, err := controllerOwnerTx(ctx, tx, team, change.Ref.RunID, now); err != nil {
		return Applied{}, err
	}
	applied, err := transitionNodeStateTx(ctx, tx, team, change, now)
	if err != nil {
		return Applied{}, err
	}
	if !applied.Replayed {
		if err = settleControllerTx(ctx, tx, team, change.Ref.RunID, tx.time); err != nil {
			return Applied{}, err
		}
	}
	return applied, nil
}

func controllerOwnerTx(ctx context.Context, tx *storeTx, team Team, runID string, now time.Time) (*Actor, error) {
	scheduled, err := controllerSchedulingTx(ctx, tx, team, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("run", runID)
	}
	if err != nil || !scheduled {
		return nil, err
	}
	var holder string
	var generation, until int64
	err = tx.QueryRowContext(ctx, `SELECT holder,generation,until FROM leases WHERE name='controller.owner'`+tx.forKeyShare()).Scan(&holder, &generation, &until)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if until <= tx.time.UnixNano() {
		return nil, nil
	}
	return &Actor{Kind: ActorController, Owner: OwnerLeaseRef{Kind: OwnerController, Name: "controller.owner", RunID: runID, Holder: holder, Generation: generation}}, nil
}

func settleControllerTx(ctx context.Context, tx *storeTx, team Team, runID string, now time.Time) error {
	actor, err := controllerOwnerTx(ctx, tx, team, runID, now)
	if err != nil || actor == nil {
		return err
	}
	for {
		snapshot, err := readSnapshotTx(ctx, tx, team, runID)
		if err != nil {
			return err
		}
		if snapshot.Run.FinishedAt != nil {
			return nil
		}
		graph, err := snapshot.Scheduling()
		if err != nil {
			return err
		}
		decision := Decide(graph)
		changed := false
		for _, node := range decision.Nodes {
			change := nodeTransition{Ref: controllerwire.NodeRef{RunID: runID, NodeID: node.NodeID}, Actor: *actor}
			switch node.Kind {
			case DecisionExpand:
				_, err = tx.ExecContext(ctx, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,owner_generation) VALUES (?,?,?,?,'pending',?) ON CONFLICT(team,run_id,source_node_id,source_generation) DO NOTHING`, team, runID, node.NodeID, node.Generation, actor.Owner.Generation)
				if err != nil {
					return err
				}
				continue
			case DecisionReady:
				change.To, change.Cause = NodeReady, CauseRelease
			case DecisionApproval:
				change.To, change.Cause = NodeApprovalPending, CauseApprovalOpen
			case DecisionTerminal:
				change.To, change.Cause = NodeState(node.Outcome), CauseDependency
			case DecisionCancelHolder:
				change.To, change.Cause = NodeCancelled, CauseCancel
			default:
				return errors.New("unknown scheduling decision")
			}
			applied, err := transitionNodeStateTx(ctx, tx, team, change, now)
			if err != nil {
				return err
			}
			changed = changed || !applied.Replayed
		}
		if decision.Outcome != "" {
			_, err = transitionRunTx(ctx, tx, team, RunTransition{RunID: runID, To: RunState(decision.Outcome), Actor: *actor, Cause: RunFinish, Reason: decision.Reason}, now)
			return err
		}
		if !changed {
			return nil
		}
	}
}
