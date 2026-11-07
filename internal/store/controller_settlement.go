package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func transitionNodeTx(ctx context.Context, tx *storeTx, team Team, change nodeTransition) (runstate.Applied, error) {
	if err := prepareStateActorTx(ctx, tx, team, string(change.Ref.RunID), change.Actor); err != nil {
		return runstate.Applied{}, err
	}
	if err := lockControllerCapacityTx(ctx, tx, team, string(change.Ref.RunID)); err != nil {
		return runstate.Applied{}, err
	}
	applied, err := transitionNodeStateTx(ctx, tx, team, change)
	if err != nil {
		return runstate.Applied{}, err
	}
	if !applied.Replayed && !applied.Waiting {
		if err = settleControllerTx(ctx, tx, team, string(change.Ref.RunID), tx.now(ctx)); err != nil {
			return runstate.Applied{}, err
		}
	}
	return applied, nil
}

// Completion can settle billing before the controller schedules again. Lock
// every run that admission can affect before that settlement takes the ledger.
func lockControllerCapacityTx(ctx context.Context, tx *storeTx, team Team, runID string) error {
	actor, err := controllerOwnerTx(ctx, tx, team, runID)
	if err != nil || actor == nil {
		return err
	}
	keys, _, err := capacityDeclarationsTx(ctx, tx, team, runstate.CapacityBinding{RunID: runID}, *actor)
	if err != nil {
		return err
	}
	return lockCapacityWorkTx(ctx, tx, team, keys, runspec.NodeRef{RunID: runspec.RunID(runID)}, false)
}

func controllerOwnerTx(ctx context.Context, tx *storeTx, team Team, runID string) (*runstate.Actor, error) {
	scheduled, err := controllerSchedulingTx(ctx, tx, team, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("run", runID)
	}
	if err != nil || !scheduled {
		return nil, err
	}
	actor := controllerActor(runID)
	return &actor, nil
}

func settleControllerTx(ctx context.Context, tx *storeTx, team Team, runID string, now time.Time) error {
	actor, err := controllerOwnerTx(ctx, tx, team, runID)
	if err != nil || actor == nil {
		return err
	}
	missed := map[string]bool{}
	for {
		snapshot, err := readSnapshotTx(ctx, tx, team, runID)
		if err != nil {
			return err
		}
		if snapshot.Run.FinishedAt != nil {
			return nil
		}
		if snapshot.Run.CancelRequestedAt == nil && snapshot.Run.Spec != nil {
			result, err := (&Tenant{s: tx.store, team: team}).admitWorkTx(ctx, tx, runstate.CapacityBinding{RunID: runID, Generation: int64(actor.Owner.Generation)}, *actor)
			if err != nil {
				return err
			}
			if result.Kind == runstate.AcquireSkipped || result.Kind == runstate.AcquireFailed {
				return settleAdmissionTx(ctx, tx, team, runstate.CapacityBinding{RunID: runID}, *actor, result)
			}
		}
		graph, err := snapshot.Scheduling()
		if err != nil {
			return err
		}
		decision := Decide(graph)
		reused, deferred, err := reuseReleasedRetryJobsTx(ctx, tx, team, snapshot.Run, decision, *actor, missed)
		if err != nil {
			return err
		}
		if reused {
			continue
		}
		changed := false
		for _, node := range decision.Nodes {
			if deferred[node.NodeID] {
				continue
			}
			change := nodeTransition{Ref: runspec.NodeRef{RunID: runspec.RunID(runID), NodeID: runspec.NodeID(node.NodeID)}, Actor: *actor}
			switch node.Kind {
			case DecisionExpand:
				permission, err := releaseEvaluationTx(ctx, tx, team, change.Ref, *actor)
				if err != nil {
					return err
				}
				if permission.Waiting {
					continue
				}
				_, err = tx.execContext(ctx, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status) VALUES (?,?,?,?,'pending') ON CONFLICT(team,run_id,source_node_id,source_generation) DO NOTHING`, team, runID, node.NodeID, node.Generation)
				if err != nil {
					return err
				}
				continue
			case DecisionReady:
				change.To, change.Cause = runstate.NodeReady, runstate.CauseRelease
			case DecisionApproval:
				handled, settled, err := scheduleApprovalEvaluationTx(ctx, tx, team, change.Ref, *actor)
				if err != nil {
					return err
				}
				if handled {
					changed = changed || settled
					continue
				}
				change.To, change.Cause = runstate.NodeApprovalPending, runstate.CauseApprovalOpen
			case DecisionTerminal:
				change.To, change.Cause = runstate.NodeState(node.Outcome), causeDependency
				change.Extra.Result = &runstate.NodeResult{Outcome: change.To, Reason: node.Reason}
			case DecisionCancelHolder:
				change.To, change.Cause = runstate.NodeCancelled, causeCancel
			default:
				return errors.New("unknown scheduling decision")
			}
			applied, err := transitionNodeStateTx(ctx, tx, team, change)
			if err != nil {
				return err
			}
			changed = changed || !applied.Replayed && !applied.Waiting
		}
		if decision.Outcome != "" {
			_, err = transitionRunTx(ctx, tx, team, runstate.RunTransition{RunID: runspec.RunID(runID), To: runstate.RunState(decision.Outcome), Actor: *actor, Cause: runstate.RunFinish, Reason: decision.Reason})
			return err
		}
		if !changed {
			return nil
		}
	}
}

// A retry probes reuse when a selected job is released, as the run host does,
// so no job reuses a result while a job it needs runs again. The probe reads
// result bytes and locks the source run, which a claim or completion cannot do
// once it holds the ledger; such jobs stay pending for the controller sweep.
func reuseReleasedRetryJobsTx(ctx context.Context, tx *storeTx, team Team, run RunRow, decision ScheduleDecision, actor runstate.Actor, missed map[string]bool) (bool, map[string]bool, error) {
	if run.Retry == nil || run.CancelRequestedAt != nil {
		return false, nil, nil
	}
	probe := tx.blobIO != nil && !tx.ledgerLocked
	deferred := map[string]bool{}
	reused := false
	var pending error
	for _, node := range decision.Nodes {
		selected, ok := run.Retry.Results[node.NodeID]
		if !ok || missed[node.NodeID] || node.Kind != DecisionReady && node.Kind != DecisionApproval {
			continue
		}
		if !probe {
			deferred[node.NodeID] = true
			continue
		}
		source := runstate.MaterializeSource{Kind: runstate.SourceManualRetry, Result: &selected}
		ok, err := tryMaterializationTx(ctx, tx, team, ReconcileRequest{Ref: runspec.NodeRef{RunID: runspec.RunID(run.ID), NodeID: runspec.NodeID(node.NodeID)}, Actor: actor, Policy: source.Kind, Source: &source})
		if errors.Is(err, errBlobIOPending) {
			pending = err
			continue
		}
		if err != nil {
			return false, nil, err
		}
		missed[node.NodeID] = !ok
		reused = reused || ok
	}
	return reused, deferred, pending
}
