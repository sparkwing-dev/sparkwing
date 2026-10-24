package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// Logical node states use the terminal outcome as the terminal state name.
const (
	nodePending runstate.NodeState = "pending"
)

// Causes distinguish retry, materialization and cancellation even when their logical states coincide.
const (
	causeDependency       runstate.TransitionCause = "dependency"
	causeOwnerPolicy      runstate.TransitionCause = "owner_policy"
	causeAdmissionRefused runstate.TransitionCause = "admission_refused"

	causeApprovalResolve runstate.TransitionCause = "approval_resolve"
	causeApprovalTimeout runstate.TransitionCause = "approval_timeout"

	causeMaterialize runstate.TransitionCause = "materialize"

	causeCancel runstate.TransitionCause = "cancel"
)

// edgeGuard names predicates that the transition re-checks after locking the row.
type edgeGuard string

// Guard values are requirements, not actor-supplied booleans.
const (
	guardLauncherAward   edgeGuard = "exact_live_launcher_award"
	guardOwner           edgeGuard = "live_owner"
	guardOwnerPolicy     edgeGuard = "typed_owner_policy"
	guardUnpriced        edgeGuard = "authorized_unpriced_claim"
	guardUnclaimed       edgeGuard = "unclaimed"
	guardHolder          edgeGuard = "live_exact_holder"
	guardAward           edgeGuard = "atomic_award"
	guardApproval        edgeGuard = "approval_resolution"
	guardApprovalTimeout edgeGuard = "approval_deadline_or_owner_deadline"
	guardQueueDeadline   edgeGuard = "queue_or_owner_deadline"
	guardMaterialize     edgeGuard = "unclaimed_or_self_claimed_and_readable_source"
	guardRetry           edgeGuard = "retryable_failure_with_budget"
	guardExpired         edgeGuard = "expired_claim"
	guardDisruptions     edgeGuard = "incremented_disruptions_below_cap"
	guardCancel          edgeGuard = "cancel_authority"
	guardCancelLapse     edgeGuard = "expired_claim_and_cancel_requested"
	guardDisruptionCap   edgeGuard = "expired_claim_increment_reaches_cap"
	guardPublication     edgeGuard = "live_run_bound_publisher_and_publish_intake"
)

// nodeEdge is a complete legal edge family; absence from the table means refusal.
type nodeEdge struct {
	From        []runstate.NodeState
	To          []runstate.NodeState
	Actors      []runstate.ActorKind
	Cause       runstate.TransitionCause
	Guards      []edgeGuard
	RequestOnly bool
}

// nodeTransitions returns fresh data so callers cannot mutate the store's legal transition table.
// Insertions start at pending with state_seq=0 through AcceptPlan or AcceptExpansion and are not edges.
func nodeTransitions() []nodeEdge {
	terminal := []runstate.NodeState{runstate.NodeSuccess, runstate.NodeFailed, runstate.NodeCancelled, runstate.NodeSkipped, runstate.NodeSuperseded, runstate.NodeMemoized}
	owners := []runstate.ActorKind{runstate.ActorController, runstate.ActorRunHost}
	cancellers := []runstate.ActorKind{runstate.ActorOperator, runstate.ActorController, runstate.ActorReaper}
	return []nodeEdge{
		{[]runstate.NodeState{runstate.NodeClaimed, runstate.NodeRunning}, []runstate.NodeState{runstate.NodeFailed}, []runstate.ActorKind{runstate.ActorLauncher}, runstate.CauseLaunchRejected, []edgeGuard{guardLauncherAward}, false},
		{[]runstate.NodeState{nodePending, runstate.NodeReady}, []runstate.NodeState{runstate.NodeFailed}, []runstate.ActorKind{runstate.ActorClaimant}, runstate.CauseUnpriced, []edgeGuard{guardUnclaimed, guardUnpriced}, false},
		{[]runstate.NodeState{nodePending}, terminal, []runstate.ActorKind{runstate.ActorOperator}, runstate.CausePublish, []edgeGuard{guardPublication}, false},
		{[]runstate.NodeState{nodePending}, []runstate.NodeState{nodePending}, []runstate.ActorKind{runstate.ActorOperator}, runstate.CausePublishAttempt, []edgeGuard{guardPublication}, false},
		{[]runstate.NodeState{nodePending}, []runstate.NodeState{runstate.NodeReady}, owners, runstate.CauseRelease, []edgeGuard{guardOwner, guardUnclaimed}, false},
		{[]runstate.NodeState{nodePending}, []runstate.NodeState{runstate.NodeSkipped, runstate.NodeCancelled}, owners, causeDependency, []edgeGuard{guardOwner, guardUnclaimed}, false},
		{[]runstate.NodeState{nodePending}, []runstate.NodeState{runstate.NodeFailed, runstate.NodeSkipped}, owners, causeOwnerPolicy, []edgeGuard{guardOwner, guardUnclaimed, guardOwnerPolicy}, false},
		{[]runstate.NodeState{nodePending}, []runstate.NodeState{runstate.NodeFailed, runstate.NodeSkipped}, owners, causeAdmissionRefused, []edgeGuard{guardOwner, guardUnclaimed}, false},
		{[]runstate.NodeState{nodePending, runstate.NodeReady}, []runstate.NodeState{runstate.NodeApprovalPending}, owners, runstate.CauseApprovalOpen, []edgeGuard{guardOwner, guardUnclaimed}, false},
		{[]runstate.NodeState{runstate.NodeApprovalPending}, []runstate.NodeState{runstate.NodeSuccess, runstate.NodeFailed}, []runstate.ActorKind{runstate.ActorOperator}, causeApprovalResolve, []edgeGuard{guardApproval}, false},
		{[]runstate.NodeState{runstate.NodeApprovalPending}, []runstate.NodeState{runstate.NodeSuccess, runstate.NodeFailed}, []runstate.ActorKind{runstate.ActorReaper, runstate.ActorRunHost}, causeApprovalTimeout, []edgeGuard{guardApprovalTimeout}, false},
		{[]runstate.NodeState{runstate.NodeReady}, []runstate.NodeState{nodePending}, owners, runstate.CauseRevoke, []edgeGuard{guardOwner, guardUnclaimed}, false},
		{[]runstate.NodeState{runstate.NodeReady}, []runstate.NodeState{runstate.NodeClaimed}, []runstate.ActorKind{runstate.ActorHolder, runstate.ActorRunHost}, runstate.CauseAward, []edgeGuard{guardAward, guardUnclaimed}, false},
		{[]runstate.NodeState{nodePending, runstate.NodeReady}, []runstate.NodeState{runstate.NodeFailed}, []runstate.ActorKind{runstate.ActorReaper}, runstate.CauseQueueTimeout, []edgeGuard{guardQueueDeadline, guardUnclaimed}, false},
		{[]runstate.NodeState{nodePending, runstate.NodeReady, runstate.NodeClaimed}, terminal, []runstate.ActorKind{runstate.ActorRunHost}, causeMaterialize, []edgeGuard{guardOwner, guardMaterialize}, false},
		{[]runstate.NodeState{runstate.NodeClaimed}, []runstate.NodeState{runstate.NodeRunning}, []runstate.ActorKind{runstate.ActorHolder}, runstate.CauseStart, []edgeGuard{guardHolder}, false},
		{[]runstate.NodeState{runstate.NodeClaimed}, []runstate.NodeState{nodePending}, []runstate.ActorKind{runstate.ActorHolder}, runstate.CauseRelease, []edgeGuard{guardHolder}, false},
		{[]runstate.NodeState{runstate.NodeClaimed}, []runstate.NodeState{nodePending}, []runstate.ActorKind{runstate.ActorReaper}, runstate.CauseLeaseLapse, []edgeGuard{guardExpired}, false},
		{[]runstate.NodeState{runstate.NodeClaimed}, terminal, []runstate.ActorKind{runstate.ActorHolder}, runstate.CauseReport, []edgeGuard{guardHolder}, false},
		{[]runstate.NodeState{runstate.NodeRunning}, []runstate.NodeState{runstate.NodeRunning}, []runstate.ActorKind{runstate.ActorHolder}, runstate.CauseRetry, []edgeGuard{guardHolder, guardRetry}, false},
		{[]runstate.NodeState{runstate.NodeRunning}, terminal, []runstate.ActorKind{runstate.ActorHolder}, runstate.CauseReport, []edgeGuard{guardHolder}, false},
		{[]runstate.NodeState{runstate.NodeRunning}, []runstate.NodeState{nodePending}, []runstate.ActorKind{runstate.ActorReaper}, runstate.CauseLeaseLapse, []edgeGuard{guardExpired, guardDisruptions}, false},
		{[]runstate.NodeState{runstate.NodeRunning}, []runstate.NodeState{runstate.NodeFailed}, []runstate.ActorKind{runstate.ActorReaper}, runstate.CauseLeaseLapse, []edgeGuard{guardDisruptionCap}, false},
		{[]runstate.NodeState{runstate.NodeClaimed, runstate.NodeRunning}, []runstate.NodeState{runstate.NodeCancelled}, []runstate.ActorKind{runstate.ActorReaper}, runstate.CauseLeaseLapse, []edgeGuard{guardCancelLapse}, false},
		{[]runstate.NodeState{nodePending, runstate.NodeReady, runstate.NodeApprovalPending}, []runstate.NodeState{runstate.NodeCancelled}, cancellers, causeCancel, []edgeGuard{guardCancel, guardUnclaimed}, false},
		{[]runstate.NodeState{runstate.NodeClaimed, runstate.NodeRunning}, []runstate.NodeState{runstate.NodeCancelled}, cancellers, causeCancel, []edgeGuard{guardCancel}, true},
		{[]runstate.NodeState{nodePending, runstate.NodeReady, runstate.NodeApprovalPending}, []runstate.NodeState{runstate.NodeCancelled}, []runstate.ActorKind{runstate.ActorRunHost}, causeCancel, []edgeGuard{guardOwner, guardCancel, guardUnclaimed}, false},
		{[]runstate.NodeState{runstate.NodeClaimed, runstate.NodeRunning}, []runstate.NodeState{runstate.NodeCancelled}, []runstate.ActorKind{runstate.ActorRunHost}, causeCancel, []edgeGuard{guardOwner, guardCancel}, true},
	}
}

// transitionExtra carries only the columns owned by a particular edge; mismatched fields are refused.
type transitionExtra struct {
	Publication *runspec.PublishedAttempt
	OwnerPolicy *runstate.OwnerPolicyResult
	Result      *runstate.NodeResult
	Materialize *runstate.MaterializeSource
	Approval    *runstate.ApprovalResolution
	Claim       *runstate.ClaimAward
	Attempt     *runstate.NodeAttempt
	RetryAt     *time.Time
}

// nodeTransition has no caller-selected event kind or clock; the edge determines both.
type nodeTransition struct {
	Ref           runspec.NodeRef
	To            runstate.NodeState
	Actor         runstate.Actor
	Cause         runstate.TransitionCause
	SourceAttempt *runspec.AttemptOrdinal
	Extra         transitionExtra
}

// IllegalTransitionReason maps stale authority to lock_held and invalid edges to illegal_transition.
type IllegalTransitionReason string

// Refusal reasons are shared by the Go error and C2's API error mapping.
const (
	ReasonHolderRights    IllegalTransitionReason = "holder_rights"
	ReasonIllegalEdge     IllegalTransitionReason = "illegal_edge"
	ReasonStaleGeneration IllegalTransitionReason = "stale_generation"
	ReasonTerminal        IllegalTransitionReason = "terminal"
)

// ErrIllegalTransition refuses a failed write loudly; a zero-row mutation is never success.
type ErrIllegalTransition struct {
	From   string
	To     string
	Actor  runstate.ActorKind
	Reason IllegalTransitionReason
}

// Error supplies a stable reason without exposing a capability or result payload.
func (e *ErrIllegalTransition) Error() string {
	return fmt.Sprintf("illegal job transition %s -> %s by %s: %s", e.From, e.To, e.Actor, e.Reason)
}

// TransitionNode changes state, attempts, events, expansion barriers, holder release and settlement atomically.
func (t *Tenant) TransitionNode(ctx context.Context, command NodeCommand) (runstate.Applied, error) {
	if nilNodeCommand(command) {
		return runstate.Applied{}, authorityError(apierr.BadRequest, "node command is required")
	}
	change := command.nodeChange()
	var out runstate.Applied
	err := t.s.inBlobTx(ctx, func(tx *storeTx) error {
		var err error

		out, err = transitionNodeTx(ctx, tx, t.team, change)
		return err
	})
	return out, err
}

// safety: an expired claim is still claimed or running until the reaper commits an edge; readers cannot release it.
func nodeState(row runstate.NodeRow) (runstate.NodeState, error) {
	switch row.Status {
	case runstate.StatusDone:
		if !slices.Contains([]runstate.NodeState{runstate.NodeSuccess, runstate.NodeFailed, runstate.NodeCancelled, runstate.NodeSkipped, runstate.NodeSuperseded, runstate.NodeMemoized}, row.Outcome) || row.ClaimedBy != "" || row.LeaseExpiresAt != nil || row.CapabilityPrefix != "" || row.OwnerCapabilityPrefix != "" || row.FinishedAt == nil {
			return "", authorityError(apierr.IntegrityFailure, "invalid terminal job state")
		}
		return row.Outcome, nil
	case runstate.StatusRunning:
		if row.ClaimedBy == "" || row.LeaseExpiresAt == nil || row.CapabilityPrefix == "" || row.Generation <= 0 || row.Attempt <= 0 {
			return "", authorityError(apierr.IntegrityFailure, "running job has no claim or attempt")
		}
		return runstate.NodeRunning, nil
	case runstate.StatusApprovalPending:
		if row.ClaimedBy != "" || row.Outcome != "" {
			return "", authorityError(apierr.IntegrityFailure, "invalid approval state")
		}
		return runstate.NodeApprovalPending, nil
	case runstate.StatusPending:
		if row.Outcome != "" || row.FinishedAt != nil {
			return "", authorityError(apierr.IntegrityFailure, "invalid pending job")
		}
		if row.ClaimedBy != "" {
			if row.LeaseExpiresAt == nil || row.CapabilityPrefix == "" || row.Generation <= 0 {
				return "", authorityError(apierr.IntegrityFailure, "incomplete job claim")
			}
			return runstate.NodeClaimed, nil
		}
		if row.LeaseExpiresAt != nil || row.CapabilityPrefix != "" || row.OwnerCapabilityPrefix != "" {
			return "", authorityError(apierr.IntegrityFailure, "orphan job claim fields")
		}
		if row.ReadyAt != nil {
			return runstate.NodeReady, nil
		}
		return nodePending, nil
	default:
		return "", authorityError(apierr.IntegrityFailure, "unknown job status")
	}
}

// safety: holder rights and replay digest are rechecked under locks using the store clock before any status write.
func transitionNodeStateTx(ctx context.Context, tx *storeTx, team Team, change nodeTransition) (runstate.Applied, error) {
	if err := validateSourceAttempt(change); err != nil {
		return runstate.Applied{}, err
	}
	if err := prepareStateActorTx(ctx, tx, team, string(change.Ref.RunID), change.Actor); err != nil {
		return runstate.Applied{}, err
	}
	if change.Cause == causeMaterialize {
		var err error
		change, err = materializeChangeTx(ctx, tx, team, change)
		if err != nil {
			return runstate.Applied{}, err
		}
	}
	if err := lockStateRunTx(ctx, tx, team, string(change.Ref.RunID)); err != nil {
		return runstate.Applied{}, err
	}
	now := tx.now(ctx)
	row, err := readNodeTx(ctx, tx, team, change.Ref)
	if err != nil {
		return runstate.Applied{}, err
	}
	from, err := nodeState(row)
	if err != nil {
		return runstate.Applied{}, err
	}
	if change.Cause == causeApprovalTimeout && change.To == "" {
		var policy string
		if err := tx.QueryRowContext(ctx, `SELECT on_timeout FROM approvals WHERE team=? AND run_id=? AND node_id=?`, team, row.Ref.RunID, row.Ref.NodeID).Scan(&policy); err != nil {
			return runstate.Applied{}, err
		}
		change.To = approvalTimeoutOutcome(policy)
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
		if change.Actor.Kind == runstate.ActorRunHost {
			if err := checkOwnerTx(ctx, tx, team, change.Actor, string(change.Ref.RunID)); err != nil {
				return runstate.Applied{}, err
			}
		}
		return runstate.Applied{Replayed: true, CancelRequested: row.CancelRequestedAt != nil && row.Status != runstate.StatusDone, StateSeq: row.StateSeq, Generation: row.Generation, Attempt: row.Attempt}, nil
	}
	if err := validateResultProofTx(ctx, tx, team, change.Actor, change.Ref, int64(row.Attempt)); err != nil {
		return runstate.Applied{}, err
	}
	if change.SourceAttempt != nil && *change.SourceAttempt != row.Attempt {
		return runstate.Applied{}, holderRefusal()
	}
	refusal := func(reason IllegalTransitionReason) (runstate.Applied, error) {
		return runstate.Applied{}, &ErrIllegalTransition{From: string(from), To: string(change.To), Actor: change.Actor.Kind, Reason: reason}
	}
	if row.Status == runstate.StatusDone {
		return refusal(ReasonTerminal)
	}
	var edge *nodeEdge
	for _, candidate := range nodeTransitions() {
		if candidate.Cause == change.Cause && slices.Contains(candidate.From, from) && slices.Contains(candidate.To, change.To) && slices.Contains(candidate.Actors, change.Actor.Kind) {
			e := candidate
			edge = &e
			break
		}
	}
	if edge == nil {
		return refusal(ReasonIllegalEdge)
	}
	if err := guardNodeEdgeTx(ctx, tx, team, row, change, *edge, now); err != nil {
		return runstate.Applied{}, err
	}
	if change.Cause == runstate.CauseStart || change.Cause == runstate.CauseRetry {
		if err := checkStartPermitTx(ctx, tx, team, row); err != nil {
			return runstate.Applied{}, err
		}
	}
	releasedEvaluation := false
	if change.To == runstate.NodeApprovalPending || change.Extra.OwnerPolicy != nil && (change.Extra.OwnerPolicy.Kind == runstate.PolicyApprovalBypass || change.Extra.OwnerPolicy.Kind == runstate.PolicyApprovalFail) {
		releasedEvaluation, err = evaluationReleasedTx(ctx, tx, team, change.Ref, "job_approval_ready", row.Generation)
		if err != nil {
			return runstate.Applied{}, err
		}
	}
	if (change.To == runstate.NodeReady || change.To == runstate.NodeApprovalPending) && !releasedEvaluation {
		admitted, err := admitReleaseTx(ctx, tx, team, string(change.Ref.RunID), change.Actor)
		if err != nil {
			return runstate.Applied{}, err
		}
		if !admitted {
			return runstate.Applied{Waiting: true}, nil
		}
	}
	if err := attachNodeResultRecordsTx(ctx, tx, team, row, change); err != nil {
		return runstate.Applied{}, err
	}
	previous := row
	if err := applyNodeEdgeTx(ctx, tx, team, &row, change, *edge, now); err != nil {
		return runstate.Applied{}, err
	}
	row.StateSeq++
	row.StateDigest = digest
	row.StateActor = change.Actor
	if err := writeNodeStateTx(ctx, tx, team, row); err != nil {
		return runstate.Applied{}, err
	}
	if !edge.RequestOnly {
		if err := persistNodeResultSelectorsTx(ctx, tx, team, row, change.Extra.Result); err != nil {
			return runstate.Applied{}, err
		}
	}
	if result := change.Extra.Result; result != nil && !edge.RequestOnly {
		var failure []byte
		if result.Failure != nil {
			if err := validateRecordedFailure(row.Ref, result.Failure); err != nil {
				return runstate.Applied{}, err
			}
			failure, err = json.Marshal(result.Failure)
			if err != nil {
				return runstate.Applied{}, err
			}
		}
		if _, err := tx.execContext(ctx, `UPDATE nodes SET error=?,failure_reason=?,exit_code=?,failure_json=? WHERE team=? AND run_id=? AND node_id=?`, result.MaskedError, result.Reason, result.ExitCode, failure, team, row.Ref.RunID, row.Ref.NodeID); err != nil {
			return runstate.Applied{}, err
		}
	} else if row.Status == runstate.StatusDone || change.To == runstate.NodeRunning {
		if _, err := tx.execContext(ctx, `UPDATE nodes SET failure_json=NULL WHERE team=? AND run_id=? AND node_id=?`, team, row.Ref.RunID, row.Ref.NodeID); err != nil {
			return runstate.Applied{}, err
		}
	}
	kind := ""
	for _, event := range nodeEvents() {
		if event.To == change.To && event.RequestOnly == edge.RequestOnly && (event.Cause == "" || event.Cause == change.Cause) {
			kind = event.Kind
			break
		}
	}
	if kind == "" {
		return runstate.Applied{}, errors.New("job transition has no event")
	}
	if err := appendStateEventTx(ctx, tx, team, change.Ref, kind, row.StateSeq, change.Extra.Result, now); err != nil {
		return runstate.Applied{}, err
	}
	if change.Cause == causeApprovalResolve || change.Cause == causeApprovalTimeout || change.Cause == causeCancel {
		actor, err := stateAuditActorTx(ctx, tx, team, change.Actor)
		if err != nil {
			return runstate.Applied{}, err
		}
		node := digestAudit(string(row.Ref.NodeID))
		var payload auditPayload = auditCancellation{RunID: string(row.Ref.RunID), Node: &node}
		if change.Cause != causeCancel {
			decision := auditRequestDecision("timed_out")
			if change.Cause == causeApprovalResolve {
				decision = auditRequestDecision(change.Extra.Approval.Decision)
			}
			payload = auditApprovalDecision{RunID: string(row.Ref.RunID), Node: node, Decision: decision}
		}
		if err := tx.operator.recordAudit(ctx, tx, auditContext{At: now, Team: team, Actor: actor}, payload); err != nil {
			return runstate.Applied{}, err
		}
	}
	if err := transitionNodeBillingTx(ctx, tx, team, previous, change, *edge, now); err != nil {
		return runstate.Applied{}, err
	}
	if row.CancelRequestedAt != nil {
		if err := clampRunLeasesTx(ctx, tx, team, string(change.Ref.RunID), string(change.Ref.NodeID)); err != nil {
			return runstate.Applied{}, err
		}
	}
	if change.Actor.Kind == runstate.ActorRunHost && change.Actor.Owner.Kind == runstate.OwnerEval {
		if err := finishApprovalEvaluationTx(ctx, tx, team, previous, change); err != nil {
			return runstate.Applied{}, err
		}
	}
	return runstate.Applied{CancelRequested: edge.RequestOnly, StateSeq: row.StateSeq, Generation: row.Generation, Attempt: row.Attempt}, nil
}

func validateSourceAttempt(change nodeTransition) error {
	required := change.Actor.Kind == runstate.ActorHolder && slices.Contains([]runstate.TransitionCause{runstate.CauseStart, runstate.CauseRetry, runstate.CauseReport, runstate.CauseRelease}, change.Cause)
	if required {
		if change.SourceAttempt == nil || *change.SourceAttempt < 0 {
			return authorityError(apierr.BadRequest, "holder transition requires a nonnegative source attempt")
		}
	} else if change.SourceAttempt != nil {
		return authorityError(apierr.BadRequest, "source attempt is only valid for holder execution transitions")
	}
	return nil
}

func validateRecordedFailure(ref runspec.NodeRef, failure *nodeproto.JobFailure) error {
	if runspec.NodeID(failure.Job) != ref.NodeID || failure.Stage == "" || failure.Error == nil {
		return authorityError(apierr.BadRequest, "failure requires its job, stage and structured error")
	}
	if int64(len(failure.Error.Message)) > limits.ErrorBytes {
		return authorityError(apierr.BadRequest, "failure error exceeds limit")
	}
	raw, err := json.Marshal(failure.Error)
	if err != nil {
		return err
	}
	_, err = apierr.DecodeInner(raw)
	return err
}

// safety: plan and expansion insertion share state_seq=0 and reject a conflicting spec hash.
func insertNodeTx(ctx context.Context, tx *storeTx, team Team, row runstate.NodeRow) error {
	if row.Ref.RunID == "" || row.Ref.NodeID == "" || row.Status != runstate.StatusPending || row.StateSeq != 0 || row.ClaimedBy != "" || row.Generation != 0 || row.SpecHash == "" {
		return errors.New("invalid initial job")
	}
	if err := enforceNodesPerRunTx(ctx, tx, team, string(row.Ref.RunID)); err != nil {
		return err
	}
	execution, err := json.Marshal(row.Execution)
	if err != nil {
		return err
	}
	requirements, err := persistClaimRequirementsTx(ctx, tx, team, row.Execution.Requires.Value)
	if err != nil {
		return err
	}
	claimContext, err := persistClaimContextTx(ctx, tx, team, string(row.Ref.RunID), capability.UnitNode, requirements, execution)
	if err != nil {
		return err
	}
	_, err = tx.execContext(ctx, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,spec_hash,execution_json,expansion,claim_requirements,claim_context,seq) VALUES (?,?,?,'pending',?,?,?,?,?,?,(SELECT COALESCE(MAX(seq),-1)+1 FROM nodes WHERE team=? AND run_id=?))`, team, row.Ref.RunID, row.Ref.NodeID, []byte(row.Spec), row.SpecHash, execution, row.Expansion, requirements, claimContext, team, row.Ref.RunID)
	return err
}

func stateHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func guardNodeEdgeTx(ctx context.Context, tx *storeTx, team Team, row runstate.NodeRow, change nodeTransition, edge nodeEdge, now time.Time) error {
	if change.Extra.Result != nil {
		if change.Extra.Result.Failure != nil && change.Extra.Result.Outcome != runstate.NodeFailed {
			return authorityError(apierr.BadRequest, "recorded failure requires a failed outcome")
		}
		if !slices.Contains([]runstate.NodeState{runstate.NodeSuccess, runstate.NodeFailed, runstate.NodeCancelled, runstate.NodeSkipped, runstate.NodeSuperseded, runstate.NodeMemoized}, change.Extra.Result.Outcome) {
			return errors.New("result requires terminal outcome")
		}
		if change.Extra.Result.Outcome != change.To && change.Cause != runstate.CauseRetry {
			return errors.New("result does not match transition")
		}
		if int64(len(change.Extra.Result.MaskedError)) > limits.ErrorBytes {
			return errors.New("job error exceeds limit")
		}
	}
	if change.Extra.Materialize != nil && change.Cause != causeMaterialize {
		return errors.New("unexpected materialization")
	}
	if change.Extra.OwnerPolicy != nil && change.Cause != causeOwnerPolicy {
		return errors.New("unexpected owner policy result")
	}
	if change.Extra.Approval != nil && change.Cause != causeApprovalResolve && change.Cause != causeApprovalTimeout {
		return errors.New("unexpected approval")
	}
	if change.Extra.Claim != nil && change.Cause != runstate.CauseAward {
		return errors.New("unexpected claim")
	}
	if change.Extra.Attempt != nil && change.Cause != runstate.CauseStart {
		return errors.New("unexpected attempt")
	}
	if change.Extra.RetryAt != nil && change.Cause != runstate.CauseRetry {
		return errors.New("unexpected retry time")
	}
	if change.Extra.Publication != nil && change.Cause != runstate.CausePublishAttempt {
		return errors.New("unexpected publication attempt")
	}
	for _, guard := range edge.Guards {
		switch guard {
		case guardOwner:
			if change.Actor.Owner.Kind == runstate.OwnerEval {
				if err := checkApprovalEvaluationTx(ctx, tx, team, row, change); err != nil {
					return err
				}
				continue
			}
			check := checkOwnerTx
			if change.Cause == causeCancel {
				check = checkOwnerLeaseTx
			}
			if err := check(ctx, tx, team, change.Actor, string(row.Ref.RunID)); err != nil {
				return err
			}
			if change.To == runstate.NodeReady {
				run, err := readRunTx(ctx, tx, team, string(row.Ref.RunID))
				if err != nil {
					return err
				}
				if runGuardRefusal(run) != "" {
					return errors.New("guard refused job release")
				}
			}
		case guardLauncherAward:
			award, err := checkLauncherActorTx(ctx, tx, team, string(row.Ref.RunID), change.Actor)
			if err != nil {
				return err
			}
			if award.Claim.Kind != capability.UnitNode || runspec.NodeID(award.Claim.NodeID) != row.Ref.NodeID {
				return holderRefusal()
			}
		case guardUnclaimed:
			if row.ClaimedBy != "" {
				return holderRefusal()
			}
		case guardUnpriced:
			if err := checkUnpricedClaimTx(ctx, tx, row, change); err != nil {
				return err
			}
		case guardOwnerPolicy:
			run, err := readRunTx(ctx, tx, team, string(row.Ref.RunID))
			if err != nil {
				return err
			}
			if run.CancelRequestedAt != nil || row.CancelRequestedAt != nil {
				return holderRefusal()
			}
			if err := checkNodeOwnerPolicy(row, change); err != nil {
				return err
			}
		case guardHolder:
			if err := checkHolderTx(ctx, tx, team, row, change.Actor); err != nil {
				return err
			}
		case guardCancel:
			if err := checkCancelActorTx(ctx, tx, team, change.Actor, string(row.Ref.RunID)); err != nil {
				return err
			}
		case guardExpired, guardDisruptions, guardCancelLapse, guardDisruptionCap:
			if row.LeaseExpiresAt == nil || row.LeaseExpiresAt.After(now) {
				return holderRefusal()
			}
			cancel := row.CancelRequestedAt != nil
			run, err := readRunTx(ctx, tx, team, string(row.Ref.RunID))
			if err != nil {
				return err
			}
			cancel = cancel || run.CancelRequestedAt != nil
			if (guard == guardCancelLapse) != cancel {
				return holderRefusal()
			}
			if guard == guardDisruptions && row.Disruptions+1 >= limits.MaxDisruptions {
				return errors.New("job disruption limit reached")
			}
			if guard == guardDisruptionCap && row.Disruptions+1 < limits.MaxDisruptions {
				return errors.New("job disruption limit not reached")
			}
		case guardRetry:
			if change.Extra.Result == nil || change.Extra.Result.Outcome != runstate.NodeFailed || row.ApplicationRetries >= row.Execution.Retry.Value || row.CancelRequestedAt != nil {
				return errors.New("job retry budget exhausted or invalid retry")
			}
		case guardAward:
			award := change.Extra.Claim
			if award == nil || award.Holder == "" || award.CapabilityPrefix == "" || award.Generation != row.Generation+1 || !award.LeaseExpiresAt.After(now) {
				return holderRefusal()
			}
			cap, err := loadCapabilityTx(ctx, tx, award.CapabilityPrefix)
			if err != nil {
				return err
			}
			awarded := runspec.NodeFence{RunID: row.Ref.RunID, NodeID: row.Ref.NodeID, Holder: award.Holder, Generation: award.Generation}
			if change.Actor.CapabilityPrefix != award.CapabilityPrefix || awarded != change.Actor.NodeFence {
				return holderRefusal()
			}
			if cap.Team != string(team) || awarded != (runspec.NodeFence{RunID: runspec.RunID(cap.Binding.RunID), NodeID: runspec.NodeID(cap.Binding.NodeID), Holder: cap.Binding.Holder, Generation: runspec.ClaimGeneration(cap.Binding.Generation)}) {
				return holderRefusal()
			}
		case guardQueueDeadline:
			run, err := readRunTx(ctx, tx, team, string(row.Ref.RunID))
			if err != nil {
				return err
			}
			wait := runClaimWait(run)
			if !wait.Set || wait.Value <= 0 || row.QueueStartedAt == nil || row.QueueStartedAt.Add(wait.Value).After(now) {
				return errors.New("queue deadline not reached")
			}
		case guardApproval, guardApprovalTimeout:
			return checkApprovalEdgeTx(ctx, tx, team, row, change, now)
		case guardMaterialize:
			if change.Extra.Materialize == nil || change.Extra.Result == nil {
				return errors.New("materialization requires transactional source read authority")
			}
		case guardPublication:
			if err := checkPublisherTx(ctx, tx, team, change.Actor, string(row.Ref.RunID)); err != nil {
				return err
			}
			if change.Cause == runstate.CausePublish && row.Attempt > 0 {
				var outcome runstate.NodeState
				if err := tx.QueryRowContext(ctx, `SELECT outcome FROM node_attempts WHERE team=? AND run_id=? AND node_id=? AND attempt=? AND generation=?`, team, row.Ref.RunID, row.Ref.NodeID, row.Attempt, row.Generation).Scan(&outcome); err != nil {
					return err
				}
				if outcome != change.To {
					return errors.New("publication result differs from its imported attempt")
				}
			}
		default:
			return errors.New("unsupported transition guard")
		}
	}
	return nil
}

func applyNodeEdgeTx(ctx context.Context, tx *storeTx, team Team, row *runstate.NodeRow, change nodeTransition, edge nodeEdge, now time.Time) error {
	if change.Cause == runstate.CausePublishAttempt {
		return importPublishedAttemptTx(ctx, tx, team, row, change)
	}
	if edge.RequestOnly {
		if row.CancelRequestedAt == nil {
			row.CancelRequestedAt = &now
			if change.Extra.Result != nil {
				row.CancelReason = change.Extra.Result.Reason
			}
		}
		return nil
	}
	if row.Status == runstate.StatusRunning && (change.To != runstate.NodeRunning || change.Cause == runstate.CauseRetry) {
		outcome, reason := change.To, ""
		if change.Extra.Result != nil {
			outcome, reason = change.Extra.Result.Outcome, change.Extra.Result.Reason
		}
		if change.Cause == runstate.CauseLeaseLapse {
			outcome, reason = runstate.NodeFailed, failureAgentLost
			if change.To == runstate.NodeCancelled {
				outcome, reason = runstate.NodeCancelled, "cancelled"
			}
		}
		result, err := tx.execContext(ctx, `UPDATE node_attempts SET finished_at=?,outcome=?,failure_reason=? WHERE team=? AND run_id=? AND node_id=? AND attempt=? AND generation=? AND finished_at IS NULL`, now.UnixNano(), outcome, reason, team, row.Ref.RunID, row.Ref.NodeID, row.Attempt, row.Generation)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("attempt closure did not match live attempt")
		}
		// Reusable memos become independent of producer retention with committed success.
		memoSQL := `DELETE FROM blob_records WHERE team=? AND kind='memo' AND run_id=? AND source_node_id=? AND source_attempt=? AND source_generation=?`
		if outcome == runstate.NodeSuccess {
			memoSQL = `UPDATE blob_records SET run_id=NULL WHERE team=? AND kind='memo' AND run_id=? AND source_node_id=? AND source_attempt=? AND source_generation=?`
		}
		if _, err := tx.execContext(ctx, memoSQL, team, row.Ref.RunID, row.Ref.NodeID, row.Attempt, row.Generation); err != nil {
			return err
		}
		stepState := stepFailed
		switch outcome {
		case runstate.NodeSuccess:
			stepState = stepPassed
		case runstate.NodeCancelled:
			stepState = stepCancelled
		}
		if _, err := tx.execContext(ctx, `UPDATE node_steps SET status=?,finished_at=? WHERE team=? AND run_id=? AND node_id=? AND attempt=? AND generation=? AND status='running'`, stepState, now.UnixNano(), team, row.Ref.RunID, row.Ref.NodeID, row.Attempt, row.Generation); err != nil {
			return err
		}
	}
	switch change.To {
	case runstate.NodeReady:
		row.ReadyAt = &now
		if row.QueueStartedAt == nil {
			row.QueueStartedAt = &now
		}
	case runstate.NodeClaimed:
		award := change.Extra.Claim
		row.ClaimedBy = award.Holder
		row.CapabilityPrefix = award.CapabilityPrefix
		row.Generation = award.Generation
		row.LeaseExpiresAt = &award.LeaseExpiresAt
	case runstate.NodeRunning:
		if _, err := tx.execContext(ctx, "UPDATE nodes SET summary='',annotations_json=NULL WHERE team=? AND run_id=? AND node_id=?", team, row.Ref.RunID, row.Ref.NodeID); err != nil {
			return err
		}
		row.Status = runstate.StatusRunning
		row.QueueStartedAt = nil
		row.Attempt++
		row.StartedAt = &now
		if a := change.Extra.Attempt; a != nil {
			if a.Identity.Team != string(team) || runspec.RunID(a.Identity.RunID) != row.Ref.RunID || runspec.NodeID(a.Identity.NodeID) != row.Ref.NodeID || runspec.AttemptOrdinal(a.Identity.Attempt) != row.Attempt || runspec.ClaimGeneration(a.Identity.Generation) != row.Generation {
				return errors.New("attempt identity does not match claim")
			}
		}
		kind, name, location, err := attemptExecutorTx(ctx, tx, change.Actor, change.Extra.Attempt)
		if err != nil {
			return err
		}
		_, err = tx.execContext(ctx, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, team, row.Ref.RunID, row.Ref.NodeID, row.Attempt, row.Generation, row.ClaimedBy, row.CapabilityPrefix, kind, name, location, now.UnixNano())
		if err != nil {
			return err
		}
		if err := createLogStreamTx(ctx, tx, blob.LogSubject{Kind: blob.LogJobAttempt, Team: string(team), RunID: string(row.Ref.RunID), Attempt: &runspec.AttemptIdentity{Team: string(team), RunID: string(row.Ref.RunID), NodeID: string(row.Ref.NodeID), Generation: int64(row.Generation), Attempt: int64(row.Attempt)}}); err != nil {
			return err
		}
	case runstate.NodeApprovalPending:
		var job nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &job); err != nil {
			return err
		}
		if job.Approval == nil {
			return errors.New("job has no approval declaration")
		}
		timeout := row.Execution.ApprovalWait.Value.Milliseconds()
		policy := job.Approval.OnTimeout
		if policy == "" {
			policy = "fail"
		}
		if _, err := tx.execContext(ctx, `INSERT INTO approvals(team,run_id,node_id,requested_at,message,timeout_ms,on_timeout) VALUES (?,?,?,?,?,?,?)`, team, row.Ref.RunID, row.Ref.NodeID, now.UnixNano(), job.Approval.Message, timeout, policy); err != nil {
			return err
		}
		row.Status = runstate.StatusApprovalPending
	case nodePending:
		if change.Cause == runstate.CauseRelease {
			if err := releaseCapacityTx(ctx, tx, team, string(row.Ref.RunID), string(row.Ref.NodeID)); err != nil {
				return err
			}
		}
		if row.Status == runstate.StatusRunning && change.Cause == runstate.CauseLeaseLapse {
			row.Disruptions++
		}
		row.Status = runstate.StatusPending
		row.StartedAt = nil
		clearNodeClaim(row)
	default:
		if change.Cause == runstate.CauseLeaseLapse && row.Status == runstate.StatusRunning && change.To == runstate.NodeFailed {
			row.Disruptions++
		}
		row.Status = runstate.StatusDone
		row.Outcome = change.To
		row.FinishedAt = &now
		row.QueueStartedAt = nil
		clearNodeClaim(row)
		if row.Expansion == runstate.ExpansionExpected && change.To != runstate.NodeSuccess && change.To != runstate.NodeMemoized {
			row.Expansion = runstate.ExpansionNone
		}
		if err := releaseCapacityTx(ctx, tx, team, string(row.Ref.RunID), string(row.Ref.NodeID)); err != nil {
			return err
		}
	}
	return nil
}

func clearNodeClaim(row *runstate.NodeRow) {
	row.ClaimedBy = ""
	row.CapabilityPrefix = ""
	row.OwnerCapabilityPrefix = ""
	row.LeaseExpiresAt = nil
	row.ReadyAt = nil
}

func writeNodeStateTx(ctx context.Context, tx *storeTx, team Team, row runstate.NodeRow) error {
	actor, err := json.Marshal(row.StateActor)
	if err != nil {
		return err
	}
	var holder any
	if row.ClaimedBy != "" {
		holder = row.ClaimedBy
	}
	result, err := tx.execContext(ctx, `UPDATE nodes SET status=?,outcome=?,claimed_by=?,capability_prefix=?,owner_capability_prefix=?,generation=?,attempt=?,disruptions=?,ready_at=?,queue_started_at=?,lease_expires_at=?,started_at=?,finished_at=?,cancel_requested_at=?,cancel_reason=?,state_seq=?,state_digest=?,state_actor=?,expansion=? WHERE team=? AND run_id=? AND node_id=? AND state_seq=?`, row.Status, row.Outcome, holder, row.CapabilityPrefix, row.OwnerCapabilityPrefix, row.Generation, row.Attempt, row.Disruptions, timeNS(row.ReadyAt), timeNS(row.QueueStartedAt), timeNS(row.LeaseExpiresAt), timeNS(row.StartedAt), timeNS(row.FinishedAt), timeNS(row.CancelRequestedAt), row.CancelReason, row.StateSeq, row.StateDigest, actor, row.Expansion, team, row.Ref.RunID, row.Ref.NodeID, row.StateSeq-1)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("job state changed during transition")
	}
	return nil
}

func timeNS(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UnixNano()
}

func appendStateEventTx(ctx context.Context, tx *storeTx, team Team, ref runspec.NodeRef, kind string, stateSeq int64, payload any, now time.Time) error {
	seq, err := appendEventTx(ctx, tx, team, string(ref.RunID), string(ref.NodeID), kind, payload, now)
	if err != nil {
		return err
	}
	_, err = tx.execContext(ctx, `UPDATE events SET state_seq=? WHERE team=? AND run_id=? AND seq=?`, stateSeq, team, ref.RunID, seq)
	return err
}

func checkApprovalEdgeTx(ctx context.Context, tx *storeTx, team Team, row runstate.NodeRow, change nodeTransition, now time.Time) error {
	var requested, timeout int64
	var policy string
	var resolved sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT requested_at,timeout_ms,on_timeout,resolved_at FROM approvals WHERE team=? AND run_id=? AND node_id=?`+tx.forUpdate(), team, row.Ref.RunID, row.Ref.NodeID).Scan(&requested, &timeout, &policy, &resolved)
	if err != nil {
		return err
	}
	if resolved.Valid {
		return errors.New("approval already resolved")
	}
	resolution := ""
	approver := change.Actor.Owner.Holder
	if change.Cause == causeApprovalTimeout {
		if timeout <= 0 || time.Unix(0, requested).Add(time.Duration(timeout)*time.Millisecond).After(now) {
			return errors.New("approval deadline not reached")
		}
		if change.Actor.Kind != runstate.ActorReaper {
			if err := checkOwnerTx(ctx, tx, team, change.Actor, string(row.Ref.RunID)); err != nil {
				return err
			}
		}
		if change.To != approvalTimeoutOutcome(policy) {
			return errors.New("approval timeout verdict mismatch")
		}
		resolution = "timed_out"
	} else {
		principal, err := tx.ResolveAuthority(ctx, change.Actor.Authority)
		if err != nil {
			return err
		}
		if principal.Capability.Team != string(team) || !slices.Contains(principal.Capability.Grants, capability.Grant("approvals.write")) {
			return holderRefusal()
		}
		if change.Extra.Approval == nil {
			return errors.New("approval decision missing")
		}
		approver = principal.Capability.Subject.ID
		resolution = change.Extra.Approval.Decision
		if (resolution != "approved" && resolution != "denied") || (resolution == "approved") != (change.To == runstate.NodeSuccess) {
			return errors.New("approval decision mismatch")
		}
	}
	comment := ""
	if change.Extra.Approval != nil {
		comment = change.Extra.Approval.Comment
	}
	_, err = tx.execContext(ctx, `UPDATE approvals SET resolved_at=?,resolution=?,approver=?,comment=? WHERE team=? AND run_id=? AND node_id=? AND resolved_at IS NULL`, now.UnixNano(), resolution, approver, comment, team, row.Ref.RunID, row.Ref.NodeID)
	return err
}

func approvalTimeoutOutcome(policy string) runstate.NodeState {
	if policy == "approve" {
		return runstate.NodeSuccess
	}
	return runstate.NodeFailed
}
