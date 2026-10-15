package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// RunState is both the persisted status and the logical state; terminal states never reopen.
type RunState string

// Run states match the final schema CHECK.
const (
	RunPending   RunState = "pending"
	RunRunning   RunState = "running"
	RunSuccess   RunState = "success"
	RunFailed    RunState = "failed"
	RunCancelled RunState = "cancelled"
)

// RunCause distinguishes replay-safe lifecycle writes from trigger recovery that keeps the same run alive.
type RunCause string

// Run causes let the store select a lifecycle event and completion desired state.
const (
	RunStart        RunCause = "start"
	RunFinish       RunCause = "finish"
	RunCancel       RunCause = "cancel"
	RunQueueTimeout RunCause = "queue_timeout"
	RunOwnerLost    RunCause = "owner_lost"
	RunPublish      RunCause = "publish"
)

// RunEdge records all legal run-state families; a cancel of live work is initially request-only.
type RunEdge struct {
	From         []RunState
	To           []RunState
	Actors       []ActorKind
	Cause        RunCause
	Precondition string
	RequestOnly  bool
}

// RunTransitions keeps publication, owner recovery and cooperative cancellation visible to implementers.
func RunTransitions() []RunEdge {
	active := []RunState{RunPending, RunRunning}
	terminal := []RunState{RunSuccess, RunFailed, RunCancelled}
	owners := []ActorKind{ActorRunHost, ActorController}
	return []RunEdge{
		{[]RunState{RunPending}, []RunState{RunRunning}, owners, RunStart, "live owner; accepted plan", false},
		{active, terminal, owners, RunFinish, "live owner; no live work or expected expansions", false},
		{active, []RunState{RunCancelled}, []ActorKind{ActorOperator, ActorController, ActorReaper}, RunCancel, "cancel authority; resolves expected expansions; live work drains", true},
		{active, []RunState{RunCancelled}, []ActorKind{ActorController, ActorReaper, ActorRunHost}, RunFinish, "cancel requested; no live work or expected expansions", false},
		{[]RunState{RunPending}, []RunState{RunFailed}, []ActorKind{ActorReaper}, RunQueueTimeout, "queue deadline elapsed; no live claim", false},
		{active, []RunState{RunFailed, RunCancelled}, []ActorKind{ActorReaper}, RunOwnerLost, "expired local owner or remote disruption cap; cancellation wins; no live work", false},
		{active, terminal, []ActorKind{ActorOperator}, RunPublish, "publish intake; authenticated run-bound publisher; results and bytes validated", false},
	}
}

// RunTransition never accepts a client clock or effect payload; those derive from committed state.
type RunTransition struct {
	RunID       string
	To          RunState
	Actor       Actor
	Cause       RunCause
	Reason      string
	MaskedError string
}

// FinishRunRequest binds the terminal verdict to the owner unit that must end in the same transaction.
type FinishRunRequest struct {
	Transition RunTransition
	Unit       OwnerLeaseRef
}

// TransitionRun applies the run edge, its event and revisioned completion intents together.
func (t *Tenant) TransitionRun(ctx context.Context, change RunTransition) (Applied, error) {
	var out Applied
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		out, err = transitionRunTx(ctx, tx, t.team, change, tx.now())
		return err
	})
	return out, err
}

// FinishRunAndUnit prevents a terminal run from leaving an executable owner claim behind.
func (t *Tenant) FinishRunAndUnit(ctx context.Context, finish FinishRunRequest) (Applied, error) {
	var out Applied
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		if finish.Unit != finish.Transition.Actor.Owner {
			return holderRefusal()
		}
		var err error
		out, err = transitionRunTx(ctx, tx, t.team, finish.Transition, tx.now())
		if err != nil {
			return err
		}
		if out.CancelRequested {
			return errors.New("run still has live work")
		}
		if finish.Unit.Kind == OwnerTrigger {
			result, err := tx.ExecContext(ctx, `UPDATE triggers SET status='done',finished_at=?,claimed_by=NULL,capability_prefix='',lease_expires_at=NULL WHERE team=? AND id=? AND generation=? AND claimed_by=?`, tx.now().UnixNano(), t.team, finish.Unit.RunID, finish.Unit.Generation, finish.Unit.Holder)
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
		}
		return nil
	})
	return out, err
}

// safety: all run status updates use this path so events and desired effect revisions cannot drift.
func transitionRunTx(ctx context.Context, tx *storeTx, team Team, change RunTransition, now time.Time) (Applied, error) {
	if err := prepareStateActorTx(ctx, tx, team, change.RunID, change.Actor, now); err != nil {
		return Applied{}, err
	}
	if err := lockStateRunTx(ctx, tx, team, change.RunID); err != nil {
		return Applied{}, err
	}
	now = tx.now()
	row, err := readRunTx(ctx, tx, team, change.RunID)
	if err != nil {
		return Applied{}, err
	}
	digest, err := stateHash(change)
	if err != nil {
		return Applied{}, err
	}
	if change.Actor.Kind == ActorReaper {
		if err := checkReaperTx(ctx, tx, change.Actor, now); err != nil {
			return Applied{}, err
		}
	}
	if row.StateDigest == digest && reflect.DeepEqual(row.StateActor, change.Actor) {
		return Applied{Replayed: true, StateSeq: row.StateSeq, CancelRequested: row.CancelRequestedAt != nil && row.FinishedAt == nil}, nil
	}
	refusal := func() (Applied, error) {
		reason := ReasonIllegalEdge
		if row.FinishedAt != nil {
			reason = ReasonTerminal
		}
		return Applied{}, &ErrIllegalTransition{From: string(row.State), To: string(change.To), Actor: change.Actor.Kind, Reason: reason}
	}
	var edge *RunEdge
	for _, candidate := range RunTransitions() {
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
		return Applied{}, err
	}
	kind := "run_started"
	if change.Cause == RunCancel {
		if row.CancelRequestedAt == nil {
			row.CancelRequestedAt = &now
		}
		if err := cancelRunNodesTx(ctx, tx, team, row.ID, change.Actor, now); err != nil {
			return Applied{}, err
		}
		kind = "run_cancel_requested"
	} else {
		if change.Cause == RunOwnerLost || change.Cause == RunQueueTimeout {
			if err := cancelRunNodesTx(ctx, tx, team, row.ID, change.Actor, now); err != nil {
				return Applied{}, err
			}
		}
		row.State = change.To
		if change.To == RunRunning {
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
		return Applied{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status=?,error=?,started_at=?,finished_at=?,cancel_requested_at=?,state_seq=?,state_digest=?,state_actor=? WHERE team=? AND id=? AND state_seq=?`, row.State, change.MaskedError, timeNS(row.StartedAt), timeNS(row.FinishedAt), timeNS(row.CancelRequestedAt), row.StateSeq, digest, actor, team, row.ID, row.StateSeq-1)
	if err != nil {
		return Applied{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Applied{}, err
	}
	if n != 1 {
		return refusal()
	}
	if err := appendStateEventTx(ctx, tx, team, NodeRef{RunID: row.ID}, kind, row.StateSeq, map[string]string{"reason": change.Reason}, now); err != nil {
		return Applied{}, err
	}
	if err := writeRunEffectsTx(ctx, tx, team, row.ID, row.State, now); err != nil {
		return Applied{}, err
	}
	if row.FinishedAt != nil {
		if err := releaseCapacityTx(ctx, tx, team, row.ID, ""); err != nil {
			return Applied{}, err
		}
		if row.State == RunSuccess {
			_, err = tx.ExecContext(ctx, `INSERT INTO pipeline_success_pins(team,pipeline,run_id,finished_at) VALUES (?,?,?,?) ON CONFLICT(team,pipeline) DO UPDATE SET run_id=excluded.run_id,finished_at=excluded.finished_at WHERE pipeline_success_pins.finished_at<excluded.finished_at`, team, row.Pipeline, row.ID, now.UnixNano())
			if err != nil {
				return Applied{}, err
			}
		}
	}
	return Applied{StateSeq: row.StateSeq, CancelRequested: change.Cause == RunCancel}, nil
}

func guardRunEdgeTx(ctx context.Context, tx *storeTx, team Team, row RunRow, change RunTransition, now time.Time) error {
	switch change.Cause {
	case RunCancel:
		return checkCancelActorTx(ctx, tx, team, change.Actor, row.ID, now)
	case RunStart:
		if row.Spec == nil {
			return errors.New("run has no accepted plan")
		}
		return checkOwnerTx(ctx, tx, team, change.Actor, row.ID, now)
	case RunFinish:
		if row.CancelRequestedAt == nil || change.To != RunCancelled {
			if err := checkOwnerTx(ctx, tx, team, change.Actor, row.ID, now); err != nil {
				return err
			}
		} else if change.Actor.Kind != ActorReaper {
			if err := checkOwnerTx(ctx, tx, team, change.Actor, row.ID, now); err != nil {
				return err
			}
		}
		return checkRunVerdictTx(ctx, tx, team, row, change.To)
	case RunOwnerLost, RunQueueTimeout:
		if row.CancelRequestedAt != nil && change.To != RunCancelled {
			return errors.New("cancellation takes precedence")
		}
		if err := checkReaperTx(ctx, tx, change.Actor, now); err != nil {
			return err
		}
		var live int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE team=? AND run_id=? AND status<>'done' AND claimed_by IS NOT NULL`, team, row.ID).Scan(&live); err != nil {
			return err
		}
		if live != 0 {
			return errors.New("run has undrained claims")
		}
		if change.Cause == RunQueueTimeout {
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
			if row.Dispatch == "controller" && row.Spec != nil {
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
	case RunPublish:
		if err := checkPublisherTx(ctx, tx, team, change.Actor, row.ID); err != nil {
			return err
		}
		if row.Spec == nil {
			return errors.New("published run has no accepted plan")
		}
		return checkRunVerdictTx(ctx, tx, team, row, change.To)
	default:
		return errors.New("unknown run transition cause")
	}
}

func checkRunVerdictTx(ctx context.Context, tx *storeTx, team Team, row RunRow, outcome RunState) error {
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE team=? AND run_id=? AND (status<>'done' OR expansion='expected')`, team, row.ID).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return errors.New("run has unsettled jobs or expansions")
	}
	if row.Spec != nil {
		snapshot, err := readSnapshotTx(ctx, tx, team, row.ID)
		if err != nil {
			return err
		}
		graph, err := snapshot.Scheduling()
		if err != nil {
			return err
		}
		decision := Decide(graph)
		if decision.Outcome != string(outcome) {
			return errors.New("run verdict disagrees with persisted jobs")
		}
	}
	if row.CancelRequestedAt != nil && outcome != RunCancelled {
		return errors.New("cancellation takes precedence")
	}
	return nil
}

func writeOwnerRecoveryTx(ctx context.Context, tx *storeTx, target expiredUnit, owner ownerRecovery, status string, actor Actor, now time.Time) error {
	var started, finished any
	if status != "pending" {
		finished = now.UnixNano()
		if owner.started.Valid {
			started = owner.started.Int64
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE triggers SET status=?,disruptions=?,claimed_by=NULL,capability_prefix='',claim_principal='',claimed_at=NULL,lease_expires_at=NULL,started_at=?,finished_at=? WHERE team=? AND id=? AND generation=?`, status, owner.disruptions, started, finished, target.team, target.ref.RunID, owner.generation)
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
	run, err := readRunTx(ctx, tx, target.team, target.ref.RunID)
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
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET state_seq=state_seq+1,state_digest=?,state_actor=? WHERE team=? AND id=?`, digest, actorJSON, target.team, target.ref.RunID); err != nil {
		return err
	}
	return appendStateEventTx(ctx, tx, target.team, NodeRef{RunID: target.ref.RunID}, "run_requeued", run.StateSeq+1, payload, now)
}

func acceptControllerPlanTx(ctx context.Context, tx *storeTx, team Team, request PlanAcceptance, controller Actor, replay bool) error {
	if err := checkOwnerTx(ctx, tx, team, controller, request.RunID, tx.now()); err != nil {
		return err
	}
	write, err := tx.ExecContext(ctx, `UPDATE triggers SET status='done',finished_at=?,claimed_by=NULL,capability_prefix='',lease_expires_at=NULL WHERE team=? AND id=? AND generation=? AND claimed_by=? AND capability_prefix=? AND status='claimed'`, tx.now().UnixNano(), team, request.RunID, request.Actor.Owner.Generation, request.Actor.Owner.Holder, request.Actor.CapabilityPrefix)
	if err != nil {
		return err
	}
	count, err := write.RowsAffected()
	if err != nil {
		return err
	}
	if count == 1 {
		return nil
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
	var started, finished any
	if status != "pending" {
		finished = now.UnixNano()
		if unit.started.Valid {
			started = unit.started.Int64
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE eval_units SET status=?,disruptions=?,claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,claimed_at=NULL,started_at=?,finished_at=? WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=? AND generation=?`, status, unit.disruptions, started, finished, target.team, target.ref.RunID, target.ref.NodeID, target.sourceGeneration, unit.generation)
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

func startOwnerUnitTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, actor Actor) error {
	var result sql.Result
	var err error
	if unit.Kind == capability.UnitEval {
		result, err = tx.ExecContext(ctx, `UPDATE eval_units SET status='running',started_at=COALESCE(started_at,?) WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=? AND generation=? AND claimed_by=? AND capability_prefix=? AND status IN ('claimed','running')`, tx.now().UnixNano(), team, unit.RunID, unit.NodeID, unit.SourceGeneration, actor.Generation, actor.Holder, actor.CapabilityPrefix)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE triggers SET started_at=COALESCE(started_at,?) WHERE team=? AND id=? AND unit_kind=? AND generation=? AND claimed_by=? AND capability_prefix=? AND status='claimed'`, tx.now().UnixNano(), team, unit.RunID, unit.Kind, actor.Generation, actor.Holder, actor.CapabilityPrefix)
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

func cancelRunNodesTx(ctx context.Context, tx *storeTx, team Team, runID string, actor Actor, now time.Time) error {
	snapshot, err := readSnapshotTx(ctx, tx, team, runID)
	if err != nil {
		return err
	}
	for _, row := range snapshot.Nodes {
		if row.Status != StatusDone {
			if _, err := transitionNodeStateTx(ctx, tx, team, NodeTransition{Ref: row.Ref, To: NodeCancelled, Actor: actor, Cause: CauseCancel}, now); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE nodes SET expansion='none' WHERE team=? AND run_id=? AND expansion='expected'`, team, runID)
	return err
}
