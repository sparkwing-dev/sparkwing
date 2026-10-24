package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// Run causes let the store select a lifecycle event and completion desired state.
const (
	runCreditRefused runstate.RunCause = "credit_refused"
)

// runEdge records all legal run-state families; a cancel of live work is initially request-only.
type runEdge struct {
	From         []runstate.RunState
	To           []runstate.RunState
	Actors       []runstate.ActorKind
	Cause        runstate.RunCause
	Precondition string
	RequestOnly  bool
}

// runTransitions keeps publication, owner recovery and cooperative cancellation visible to implementers.
func runTransitions() []runEdge {
	active := []runstate.RunState{runstate.RunPending, runstate.RunRunning}
	terminal := []runstate.RunState{runstate.RunSuccess, runstate.RunFailed, runstate.RunCancelled}
	owners := []runstate.ActorKind{runstate.ActorRunHost, runstate.ActorController}
	return []runEdge{
		{[]runstate.RunState{runstate.RunPending}, []runstate.RunState{runstate.RunFailed}, []runstate.ActorKind{runstate.ActorOperator}, runCreditRefused, "App admission; unstarted and unfunded", false},
		{active, []runstate.RunState{runstate.RunFailed}, []runstate.ActorKind{runstate.ActorLauncher}, runstate.RunLaunchRejected, "exact live launcher plan award", false},
		{[]runstate.RunState{runstate.RunPending}, []runstate.RunState{runstate.RunRunning}, owners, runstate.RunStart, "live owner; accepted plan", false},
		{active, terminal, owners, runstate.RunFinish, "live owner; no live work or expected expansions", false},
		{active, []runstate.RunState{runstate.RunCancelled}, []runstate.ActorKind{runstate.ActorOperator, runstate.ActorController, runstate.ActorReaper}, runstate.RunCancel, "cancel authority; resolves expected expansions; live work drains", true},
		{active, []runstate.RunState{runstate.RunCancelled}, []runstate.ActorKind{runstate.ActorController, runstate.ActorReaper, runstate.ActorRunHost}, runstate.RunFinish, "cancel requested; no live work or expected expansions", false},
		{[]runstate.RunState{runstate.RunPending}, []runstate.RunState{runstate.RunFailed}, []runstate.ActorKind{runstate.ActorReaper}, runstate.RunQueueTimeout, "queue deadline elapsed; no live claim", false},
		{active, []runstate.RunState{runstate.RunFailed, runstate.RunCancelled}, []runstate.ActorKind{runstate.ActorReaper}, runstate.RunOwnerLost, "expired local owner or remote disruption cap; cancellation wins; no live work", false},
		{active, terminal, []runstate.ActorKind{runstate.ActorOperator}, runstate.RunPublish, "publish intake; authenticated run-bound publisher; results and bytes validated", false},
	}
}

// TransitionRun applies the run edge, its event and revisioned completion intents together.
func (t *Tenant) TransitionRun(ctx context.Context, change runstate.RunTransition) (runstate.Applied, error) {
	var out runstate.Applied
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		out, err = transitionRunTx(ctx, tx, t.team, change)
		return err
	})
	return out, err
}

// FinishRunAndUnit prevents a terminal run from leaving an executable owner claim behind.
func (t *Tenant) FinishRunAndUnit(ctx context.Context, finish runstate.FinishRunRequest) (runstate.Applied, error) {
	if finish.Transition.Cause != runstate.RunFinish {
		return runstate.Applied{}, errors.New("run completion requires finish")
	}

	var out runstate.Applied
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		if finish.Unit != finish.Transition.Actor.Owner {
			return holderRefusal()
		}
		var err error
		change := runstate.RunTransition{RunID: finish.Transition.RunID, Actor: finish.Transition.Actor, Cause: finish.Transition.Cause, Reason: finish.Transition.Reason, MaskedError: finish.Transition.MaskedError}
		out, err = transitionRunTx(ctx, tx, t.team, change)
		if err != nil {
			return err
		}
		if out.CancelRequested {
			return errors.New("run still has live work")
		}
		if finish.Unit.Kind == runstate.OwnerTrigger {
			result, err := tx.execContext(ctx, `UPDATE triggers SET status='done',finished_at=?,claimed_by=NULL,capability_prefix='',claim_principal='',claimed_at=NULL,lease_expires_at=NULL WHERE team=? AND id=? AND generation=? AND claimed_by=?`, tx.now(ctx).UnixNano(), t.team, finish.Unit.RunID, finish.Unit.Generation, finish.Unit.Holder)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 && !out.Replayed {
				return holderRefusal()
			}
			if err := settleOwnerBillingTx(ctx, tx, t.team, string(finish.Unit.RunID), int64(finish.Unit.Generation), tx.now(ctx)); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// safety: all run status updates use this path so events and desired effect revisions cannot drift.
func transitionRunTx(ctx context.Context, tx *storeTx, team Team, change runstate.RunTransition) (runstate.Applied, error) {
	if err := prepareStateActorTx(ctx, tx, team, string(change.RunID), change.Actor); err != nil {
		return runstate.Applied{}, err
	}
	if err := lockStateRunTx(ctx, tx, team, string(change.RunID)); err != nil {
		return runstate.Applied{}, err
	}
	now := tx.now(ctx)
	row, err := readRunTx(ctx, tx, team, string(change.RunID))
	if err != nil {
		return runstate.Applied{}, err
	}
	if change.Actor.Kind == runstate.ActorRunHost {
		principal, err := tx.ResolveAuthority(ctx, change.Actor.Authority)
		if err != nil {
			return runstate.Applied{}, err
		}
		if principal.Capability.Unit == capability.UnitPlan {
			accepted, err := planAcceptedTx(ctx, tx, team, row.ID)
			if err != nil {
				return runstate.Applied{}, err
			}
			if accepted || change.Cause != runstate.RunFinish {
				return runstate.Applied{}, holderRefusal()
			}
			if err := cancelRunNodesTx(ctx, tx, team, row.ID, change.Actor, change.Reason, now); err != nil {
				return runstate.Applied{}, err
			}
			change.Reason, change.MaskedError = string(apierr.PlanFailed), "run preparation failed"
			if row.CancelRequestedAt != nil {
				change.Reason, change.MaskedError = string(apierr.ReasonCancelled), "cancellation requested"
			}
		}
	}
	if err := validateResultProofTx(ctx, tx, team, change.Actor, runspec.NodeRef{RunID: change.RunID}, 0); err != nil {
		return runstate.Applied{}, err
	}
	if change.Cause == runstate.RunFinish || change.Cause == runstate.RunPublish {
		outcome, err := runVerdictTx(ctx, tx, team, row)
		if err != nil {
			return runstate.Applied{}, err
		}
		change.To = outcome
		if outcome == runstate.RunCancelled && row.CancelRequestedAt != nil {
			change.Reason = row.CancelReason
		}
	}
	digest, err := stateHash(change)
	if err != nil {
		return runstate.Applied{}, err
	}
	if change.Actor.Kind == runstate.ActorReaper {
		if err := checkReaperTx(ctx, tx, change.Actor); err != nil {
			return runstate.Applied{}, err
		}
	}
	if row.StateDigest == digest && reflect.DeepEqual(row.StateActor, change.Actor) {
		return runstate.Applied{Replayed: true, StateSeq: row.StateSeq, Outcome: row.State, Reason: change.Reason, CancelRequested: row.CancelRequestedAt != nil && row.FinishedAt == nil}, nil
	}
	refusal := func() (runstate.Applied, error) {
		reason := ReasonIllegalEdge
		if row.FinishedAt != nil {
			reason = ReasonTerminal
		}
		return runstate.Applied{}, &ErrIllegalTransition{From: string(row.State), To: string(change.To), Actor: change.Actor.Kind, Reason: reason}
	}
	var edge *runEdge
	for _, candidate := range runTransitions() {
		if candidate.Cause == change.Cause && slices.Contains(candidate.From, row.State) && slices.Contains(candidate.To, change.To) && slices.Contains(candidate.Actors, change.Actor.Kind) {
			e := candidate
			edge = &e
			break
		}
	}
	if edge == nil {
		return refusal()
	}
	if err := guardRunEdgeTx(ctx, tx, team, row, change, now); err != nil {
		return runstate.Applied{}, err
	}
	kind := "run_started"
	if change.Cause == runstate.RunCancel {
		if row.CancelRequestedAt == nil {
			row.CancelRequestedAt = &now
			row.CancelReason = change.Reason
		}
		if err := cancelRunNodesTx(ctx, tx, team, row.ID, change.Actor, row.CancelReason, now); err != nil {
			return runstate.Applied{}, err
		}
		if err := endUnstartedBillingTx(ctx, tx, team, row.ID, nil); err != nil {
			return runstate.Applied{}, err
		}
		kind = "run_cancel_requested"
	} else {
		if change.Cause == runstate.RunOwnerLost || change.Cause == runstate.RunQueueTimeout {
			if err := cancelRunNodesTx(ctx, tx, team, row.ID, change.Actor, row.CancelReason, now); err != nil {
				return runstate.Applied{}, err
			}
		}
		row.State = change.To
		if change.To == runstate.RunRunning {
			row.StartedAt = &now
		} else {
			row.FinishedAt = &now
			kind = "run_" + string(change.To)
		}
	}
	row.StateSeq++
	row.StateDigest = digest
	row.StateActor = change.Actor
	actor, err := json.Marshal(change.Actor)
	if err != nil {
		return runstate.Applied{}, err
	}
	result, err := tx.execContext(ctx, `UPDATE runs SET status=?,error=?,started_at=?,finished_at=?,cancel_requested_at=?,cancel_reason=?,state_seq=?,state_digest=?,state_actor=? WHERE team=? AND id=? AND state_seq=?`, row.State, change.MaskedError, timeNS(row.StartedAt), timeNS(row.FinishedAt), timeNS(row.CancelRequestedAt), row.CancelReason, row.StateSeq, digest, actor, team, row.ID, row.StateSeq-1)
	if err != nil {
		return runstate.Applied{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return runstate.Applied{}, err
	}
	if n != 1 {
		return refusal()
	}
	if row.CancelRequestedAt != nil {
		if err := clampRunLeasesTx(ctx, tx, team, row.ID, ""); err != nil {
			return runstate.Applied{}, err
		}
	}
	if err := appendStateEventTx(ctx, tx, team, runspec.NodeRef{RunID: runspec.RunID(row.ID)}, kind, row.StateSeq, map[string]string{"reason": change.Reason}, now); err != nil {
		return runstate.Applied{}, err
	}
	if err := writeRunEffectsTx(ctx, tx, team, row.ID, row.State, now); err != nil {
		return runstate.Applied{}, err
	}
	if row.FinishedAt != nil {
		tx.finishedLocalSecrets = append(tx.finishedLocalSecrets, localSecretKey{team, row.ID})
		if err := releaseCapacityTx(ctx, tx, team, row.ID, ""); err != nil {
			return runstate.Applied{}, err
		}
		if row.State == runstate.RunSuccess {
			result, err := tx.execContext(ctx, `INSERT INTO team_first_success(team,run_id) VALUES (?,?) ON CONFLICT(team) DO NOTHING`, team, row.ID)
			if err != nil {
				return runstate.Applied{}, err
			}
			first, err := result.RowsAffected()
			if err != nil {
				return runstate.Applied{}, err
			}
			if first == 1 {
				if err := tx.operator.recordAudit(ctx, tx, auditContext{At: now, Team: team, Actor: auditActor{system: auditController}}, auditFirstSuccess{RunID: row.ID}); err != nil {
					return runstate.Applied{}, err
				}
			}
			if row.Request.Request.Mode != runspec.IntakePublish {
				_, err = tx.execContext(ctx, `INSERT INTO pipeline_success_pins(team,pipeline,run_id,finished_at) VALUES (?,?,?,?) ON CONFLICT(team,pipeline) DO UPDATE SET run_id=excluded.run_id,finished_at=excluded.finished_at WHERE pipeline_success_pins.finished_at<excluded.finished_at`, team, row.Pipeline, row.ID, now.UnixNano())
				if err != nil {
					return runstate.Applied{}, err
				}
			}
		}
	}
	return runstate.Applied{StateSeq: row.StateSeq, Outcome: row.State, Reason: change.Reason, CancelRequested: change.Cause == runstate.RunCancel}, nil
}

func guardRunEdgeTx(ctx context.Context, tx *storeTx, team Team, row RunRow, change runstate.RunTransition, now time.Time) error {
	switch change.Cause {
	case runCreditRefused:
		if tx.appAdmission == nil || row.StartedAt != nil || row.Spec != nil || row.StateSeq != 0 || change.Actor.Authority != tx.appAdmission.authority() {
			return holderRefusal()
		}
		return nil
	case runstate.RunLaunchRejected:
		award, err := checkLauncherActorTx(ctx, tx, team, row.ID, change.Actor)
		if err != nil {
			return err
		}
		if award.Claim.Kind != capability.UnitPlan {
			return holderRefusal()
		}
		return nil
	case runstate.RunCancel:
		return checkCancelActorTx(ctx, tx, team, change.Actor, row.ID)
	case runstate.RunStart:
		if row.Spec == nil {
			return errors.New("run has no accepted plan")
		}
		return checkOwnerTx(ctx, tx, team, change.Actor, row.ID)
	case runstate.RunFinish:
		if row.CancelRequestedAt == nil || change.To != runstate.RunCancelled {
			if err := checkOwnerLeaseTx(ctx, tx, team, change.Actor, row.ID); err != nil {
				return err
			}
		} else if change.Actor.Kind != runstate.ActorReaper {
			if err := checkOwnerLeaseTx(ctx, tx, team, change.Actor, row.ID); err != nil {
				return err
			}
		}

		return nil
	case runstate.RunOwnerLost, runstate.RunQueueTimeout:
		if row.CancelRequestedAt != nil && change.To != runstate.RunCancelled {
			return errors.New("cancellation takes precedence")
		}
		if err := checkReaperTx(ctx, tx, change.Actor); err != nil {
			return err
		}
		var live int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE team=? AND run_id=? AND status<>'done' AND claimed_by IS NOT NULL`, team, row.ID).Scan(&live); err != nil {
			return err
		}
		if live != 0 {
			return errors.New("run has undrained claims")
		}
		if change.Cause == runstate.RunQueueTimeout {
			var liveOwners int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM triggers WHERE team=? AND id=? AND status='claimed' AND lease_expires_at>?`, team, row.ID, now.UnixNano()).Scan(&liveOwners); err != nil {
				return err
			}
			if liveOwners != 0 {
				return errors.New("run has a live owner claim")
			}
			wait := runClaimWait(row)
			if !wait.Set || wait.Value <= 0 || row.CreatedAt.Add(wait.Value).After(now) {
				return errors.New("run queue deadline not reached")
			}
		} else {
			if row.Owner != nil && row.Owner.Kind == runstate.OwnerController {
				return errors.New("controller ownership expiry does not fail a run")
			}
			if row.Owner == nil {
				return holderRefusal()
			}
			var until sql.NullInt64
			var started sql.NullInt64
			var status string
			var disruptions int
			if err := tx.QueryRowContext(ctx, `SELECT lease_expires_at,disruptions,status,started_at FROM triggers WHERE team=? AND id=?`, team, row.ID).Scan(&until, &disruptions, &status, &started); err != nil {
				return err
			}
			if status != "failed" && row.Request.Request.Origin != "local" && disruptions < limits.MaxDisruptions && row.CancelRequestedAt == nil {
				return errors.New("remote owner loss must requeue within the same run")
			}
			if until.Valid && until.Int64 > now.UnixNano() {
				return holderRefusal()
			}
			if status != "failed" && !(status == "done" && row.CancelRequestedAt != nil) && (!until.Valid || !started.Valid && row.CancelRequestedAt == nil) {
				return holderRefusal()
			}
		}
		return nil
	case runstate.RunPublish:
		if err := checkPublisherTx(ctx, tx, team, change.Actor, row.ID); err != nil {
			return err
		}
		if row.Spec == nil {
			return errors.New("published run has no accepted plan")
		}
		return nil
	default:
		return errors.New("unknown run transition cause")
	}
}

func runVerdictTx(ctx context.Context, tx *storeTx, team Team, row RunRow) (runstate.RunState, error) {
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE team=? AND run_id=? AND (status<>'done' OR expansion='expected')`, team, row.ID).Scan(&pending); err != nil {
		return "", err
	}
	if pending != 0 {
		return "", errors.New("run has unsettled jobs or expansions")
	}
	accepted, err := planAcceptedTx(ctx, tx, team, row.ID)
	if err != nil {
		return "", err
	}
	if row.Spec == nil || row.Dispatch == string(runspec.DispatchController) && row.Request.Request.Mode != runspec.IntakePublish && !accepted {
		if row.CancelRequestedAt != nil {
			return runstate.RunCancelled, nil
		}
		return runstate.RunFailed, nil
	}
	snapshot, err := readSnapshotTx(ctx, tx, team, row.ID)
	if err != nil {
		return "", err
	}
	graph, err := snapshot.Scheduling()
	if err != nil {
		return "", err
	}
	decision := Decide(graph)
	switch runstate.RunState(decision.Outcome) {
	case runstate.RunSuccess, runstate.RunFailed, runstate.RunCancelled:
		return runstate.RunState(decision.Outcome), nil
	default:
		return "", errors.New("run has no terminal verdict")
	}
}

func writeOwnerRecoveryTx(ctx context.Context, tx *storeTx, target expiredUnit, owner ownerRecovery, status string, actor runstate.Actor, now time.Time) error {
	if err := settleOwnerBillingTx(ctx, tx, target.team, string(target.ref.RunID), owner.generation, billingRecoveryEnd(owner, now)); err != nil {
		return err
	}
	var started, finished any
	if status != "pending" {
		finished = now.UnixNano()
		if owner.started.Valid {
			started = owner.started.Int64
		}
	}
	result, err := tx.execContext(ctx, `UPDATE triggers SET status=?,disruptions=?,claimed_by=NULL,capability_prefix='',claim_principal='',claimed_at=NULL,lease_expires_at=NULL,started_at=?,finished_at=? WHERE team=? AND id=? AND generation=?`, status, owner.disruptions, started, finished, target.team, target.ref.RunID, owner.generation)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return holderRefusal()
	}
	if status != "pending" {
		return nil
	}
	run, err := readRunTx(ctx, tx, target.team, string(target.ref.RunID))
	if err != nil {
		return err
	}
	if run.FinishedAt != nil || run.CancelRequestedAt != nil {
		return holderRefusal()
	}
	payload := struct {
		Unit        string `json:"unit"`
		Generation  int64  `json:"generation"`
		Disruptions int    `json:"disruptions"`
	}{string(target.kind), owner.generation, owner.disruptions}
	digest, err := stateHash(payload)
	if err != nil {
		return err
	}
	actorJSON, err := json.Marshal(actor)
	if err != nil {
		return err
	}
	if _, err := tx.execContext(ctx, `UPDATE runs SET state_seq=state_seq+1,state_digest=?,state_actor=? WHERE team=? AND id=?`, digest, actorJSON, target.team, target.ref.RunID); err != nil {
		return err
	}
	return appendStateEventTx(ctx, tx, target.team, runspec.NodeRef{RunID: target.ref.RunID}, "run_requeued", run.StateSeq+1, payload, now)
}

func acceptControllerPlanTx(ctx context.Context, tx *storeTx, team Team, request PlanAcceptance, controller runstate.Actor, replay bool) error {
	if err := checkOwnerTx(ctx, tx, team, controller, request.RunID); err != nil {
		return err
	}
	write, err := tx.execContext(ctx, `UPDATE triggers SET status='done',finished_at=?,claimed_by=NULL,capability_prefix='',claim_principal='',claimed_at=NULL,lease_expires_at=NULL WHERE team=? AND id=? AND generation=? AND claimed_by=? AND capability_prefix=? AND status='claimed'`, tx.now(ctx).UnixNano(), team, request.RunID, request.Actor.Owner.Generation, request.Actor.Owner.Holder, request.Actor.CapabilityPrefix)
	if err != nil {
		return err
	}
	count, err := write.RowsAffected()
	if err != nil {
		return err
	}
	if count == 1 {
		return settleOwnerBillingTx(ctx, tx, team, request.RunID, int64(request.Actor.Owner.Generation), tx.now(ctx))
	}
	if !replay {
		return holderRefusal()
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM triggers WHERE team=? AND id=? AND generation=?`, team, request.RunID, request.Actor.Owner.Generation).Scan(&status); err != nil {
		return err
	}
	if status != "done" {
		return holderRefusal()
	}
	return nil
}

func writeEvalRecoveryTx(ctx context.Context, tx *storeTx, target expiredUnit, unit ownerRecovery, status string, now time.Time) error {
	if err := settleUnitBillingTx(ctx, tx, target.team, UnitRef{Kind: capability.UnitEval, RunID: target.ref.RunID, NodeID: target.ref.NodeID, SourceGeneration: target.sourceGeneration}, unit.generation, billingRecoveryEnd(unit, now)); err != nil {
		return err
	}
	var started, finished any
	if status != "pending" {
		finished = now.UnixNano()
		if unit.started.Valid {
			started = unit.started.Int64
		}
	}
	result, err := tx.execContext(ctx, `UPDATE eval_units SET status=?,disruptions=?,claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,claimed_at=NULL,started_at=?,finished_at=? WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=? AND generation=?`, status, unit.disruptions, started, finished, target.team, target.ref.RunID, target.ref.NodeID, target.sourceGeneration, unit.generation)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return holderRefusal()
	}
	return nil
}

func startOwnerUnitTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, actor runstate.Actor) error {
	var result sql.Result
	var err error
	now := tx.now(ctx)
	if unit.Kind == capability.UnitEval {
		result, err = tx.execContext(ctx, `UPDATE eval_units SET status='running',started_at=COALESCE(started_at,?) WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=? AND generation=? AND claimed_by=? AND capability_prefix=? AND status IN ('claimed','running')`, now.UnixNano(), team, unit.RunID, unit.NodeID, unit.SourceGeneration, actor.Generation, actor.Holder, actor.CapabilityPrefix)
	} else {
		result, err = tx.execContext(ctx, `UPDATE triggers SET started_at=COALESCE(started_at,?) WHERE team=? AND id=? AND unit_kind=? AND generation=? AND claimed_by=? AND capability_prefix=? AND status='claimed'`, now.UnixNano(), team, unit.RunID, unit.Kind, actor.Generation, actor.Holder, actor.CapabilityPrefix)
	}
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return holderRefusal()
	}
	return nil
}

func cancelRunNodesTx(ctx context.Context, tx *storeTx, team Team, runID string, actor runstate.Actor, reason string, now time.Time) error {
	actor.Result = nil
	snapshot, err := readSnapshotTx(ctx, tx, team, runID)
	if err != nil {
		return err
	}
	for _, row := range snapshot.Nodes {
		if row.Status != runstate.StatusDone {
			if _, err := transitionNodeStateTx(ctx, tx, team, nodeTransition{Ref: row.Ref, To: runstate.NodeCancelled, Actor: actor, Cause: causeCancel, Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeCancelled, Reason: reason}}}); err != nil {
				return err
			}
		}
	}
	_, err = tx.execContext(ctx, `UPDATE nodes SET expansion='none' WHERE team=? AND run_id=? AND expansion='expected'`, team, runID)
	return err
}
