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

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// NodeState separates offerability and claim ownership from the persisted status.
type NodeState string

// Logical node states use the terminal outcome as the terminal state name.
const (
	NodePending         NodeState = "pending"
	NodeReady           NodeState = "ready"
	NodeClaimed         NodeState = "claimed"
	NodeRunning         NodeState = "running"
	NodeApprovalPending NodeState = "approval_pending"
	NodeSuccess         NodeState = "success"
	NodeFailed          NodeState = "failed"
	NodeCancelled       NodeState = "cancelled"
	NodeSkipped         NodeState = "skipped"
	NodeSuperseded      NodeState = "superseded"
	NodeMemoized        NodeState = "memoized"
)

// NodeStatus is the persisted status; ready and claimed are derived by nodeState.
type NodeStatus string

// Persisted node status values match the schema CHECK.
const (
	StatusPending         NodeStatus = "pending"
	StatusRunning         NodeStatus = "running"
	StatusDone            NodeStatus = "done"
	StatusApprovalPending NodeStatus = "approval_pending"
)

// ExpansionState prevents settlement before generated jobs have been accepted.
type ExpansionState string

// Expansion outcomes distinguish an absent generator from one that will never run.
const (
	ExpansionAbsent   ExpansionState = ""
	ExpansionExpected ExpansionState = "expected"
	ExpansionAccepted ExpansionState = "accepted"
	ExpansionFailed   ExpansionState = "failed"
	ExpansionNone     ExpansionState = "none"
)

// ActorKind identifies which store-verified authority may take an edge.
type ActorKind string

// Actor kinds never grant authority without the corresponding transactional proof.
const (
	ActorRunHost    ActorKind = "run_host"
	ActorHolder     ActorKind = "holder"
	ActorController ActorKind = "controller"
	ActorReaper     ActorKind = "reaper"
	ActorOperator   ActorKind = "operator"
)

// OwnerLeaseKind selects the persisted liveness row; local hosts use a trigger lease too.
type OwnerLeaseKind string

// Owner lease kinds keep eval claim liveness separate from its terminal source job.
const (
	OwnerTrigger    OwnerLeaseKind = "trigger"
	OwnerController OwnerLeaseKind = "controller"
	OwnerEval       OwnerLeaseKind = "eval"
)

// OwnerLeaseRef identifies a fence to re-read under the store clock, never a caller's liveness assertion.
type OwnerLeaseRef struct {
	Kind             OwnerLeaseKind
	Name             string
	RunID            string
	SourceNodeID     string
	SourceGeneration int64
	Holder           string
	Generation       int64
}

// Actor carries server-derived tagged authority; C2 resolves it through the current
// transaction. CapabilityPrefix is a separate claim fence and never substitutes
// for a session, console or peer authority reference.
type Actor struct {
	Authority        capability.AuthorityRef
	Kind             ActorKind
	CapabilityPrefix string
	Holder           string
	Generation       int64
	Owner            OwnerLeaseRef
}

// NodeRef omits team because a Tenant supplies it; a wire encoder uses job_id.
type NodeRef struct {
	RunID  string `json:"run_id"`
	NodeID string `json:"job_id"`
}

// NodeRow contains the state facts consumed by the shared decision and reconciliation functions.
type NodeRow struct {
	Ref                   NodeRef
	Status                NodeStatus
	Outcome               NodeState
	StatusDetail          string
	ClaimedBy             string
	CapabilityPrefix      string
	OwnerCapabilityPrefix string
	Generation            int64
	Attempt               int64
	ApplicationRetries    int
	Disruptions           int
	ReadyAt               *time.Time
	QueueStartedAt        *time.Time
	LeaseExpiresAt        *time.Time
	StartedAt             *time.Time
	FinishedAt            *time.Time
	CancelRequestedAt     *time.Time
	StateSeq              int64
	StateDigest           string
	StateActor            Actor
	Expansion             ExpansionState
	ExpansionDigest       string
	Execution             runspec.JobExecutionSettings
	Spec                  json.RawMessage
	SpecHash              string
}

// NodeAttempt preserves the node-wide ordinal across claims; generation is an attribute of that ordinal.
type NodeAttempt struct {
	Identity         runspec.AttemptIdentity
	Holder           string
	CapabilityPrefix string
	ExecutorKind     string
	ExecutorName     string
	ExecutorLocation string
	StartedAt        time.Time
	FinishedAt       *time.Time
	Outcome          NodeState
	FailureReason    string
}

// TransitionCause selects edge-specific validation rather than accepting arbitrary column updates.
type TransitionCause string

// Causes distinguish retry, materialization and cancellation even when their logical states coincide.
const (
	CauseRelease         TransitionCause = "release"
	CauseDependency      TransitionCause = "dependency"
	CauseApprovalOpen    TransitionCause = "approval_open"
	CauseApprovalResolve TransitionCause = "approval_resolve"
	CauseApprovalTimeout TransitionCause = "approval_timeout"
	CauseRevoke          TransitionCause = "revoke"
	CauseAward           TransitionCause = "award"
	CauseQueueTimeout    TransitionCause = "queue_timeout"
	CauseMaterialize     TransitionCause = "materialize"
	CauseStart           TransitionCause = "start"
	CauseReport          TransitionCause = "report"
	CauseRetry           TransitionCause = "retry"
	CauseLeaseLapse      TransitionCause = "lease_lapse"
	CauseCancel          TransitionCause = "cancel"
	CausePublish         TransitionCause = "publish"
)

// EdgeGuard names predicates that the transition re-checks after locking the row.
type EdgeGuard string

// Guard values are requirements, not actor-supplied booleans.
const (
	GuardOwner           EdgeGuard = "live_owner"
	GuardUnclaimed       EdgeGuard = "unclaimed"
	GuardHolder          EdgeGuard = "live_exact_holder"
	GuardAward           EdgeGuard = "atomic_award"
	GuardApproval        EdgeGuard = "approval_resolution"
	GuardApprovalTimeout EdgeGuard = "approval_deadline_or_owner_deadline"
	GuardQueueDeadline   EdgeGuard = "queue_or_owner_deadline"
	GuardMaterialize     EdgeGuard = "unclaimed_or_self_claimed_and_readable_source"
	GuardRetry           EdgeGuard = "retryable_failure_with_budget"
	GuardExpired         EdgeGuard = "expired_claim"
	GuardDisruptions     EdgeGuard = "incremented_disruptions_below_cap"
	GuardCancel          EdgeGuard = "cancel_authority"
	GuardCancelLapse     EdgeGuard = "expired_claim_and_cancel_requested"
	GuardDisruptionCap   EdgeGuard = "expired_claim_increment_reaches_cap"
	GuardPublication     EdgeGuard = "live_run_bound_publisher_and_publish_intake"
)

// NodeEdge is a complete legal edge family; absence from the table means refusal.
type NodeEdge struct {
	From        []NodeState
	To          []NodeState
	Actors      []ActorKind
	Cause       TransitionCause
	Guards      []EdgeGuard
	RequestOnly bool
}

// NodeTransitions returns fresh data so callers cannot mutate the store's legal transition table.
// Insertions start at pending with state_seq=0 through AcceptPlan or AcceptExpansion and are not edges.
func NodeTransitions() []NodeEdge {
	terminal := []NodeState{NodeSuccess, NodeFailed, NodeCancelled, NodeSkipped, NodeSuperseded, NodeMemoized}
	owners := []ActorKind{ActorController, ActorRunHost}
	cancellers := []ActorKind{ActorOperator, ActorController, ActorReaper}
	return []NodeEdge{
		{[]NodeState{NodePending}, terminal, []ActorKind{ActorOperator}, CausePublish, []EdgeGuard{GuardPublication}, false},
		{[]NodeState{NodePending}, []NodeState{NodeReady}, owners, CauseRelease, []EdgeGuard{GuardOwner, GuardUnclaimed}, false},
		{[]NodeState{NodePending}, []NodeState{NodeSkipped, NodeCancelled}, owners, CauseDependency, []EdgeGuard{GuardOwner, GuardUnclaimed}, false},
		{[]NodeState{NodePending, NodeReady}, []NodeState{NodeApprovalPending}, owners, CauseApprovalOpen, []EdgeGuard{GuardOwner, GuardUnclaimed}, false},
		{[]NodeState{NodeApprovalPending}, []NodeState{NodeSuccess, NodeFailed}, []ActorKind{ActorOperator}, CauseApprovalResolve, []EdgeGuard{GuardApproval}, false},
		{[]NodeState{NodeApprovalPending}, []NodeState{NodeSuccess, NodeFailed}, []ActorKind{ActorReaper, ActorRunHost}, CauseApprovalTimeout, []EdgeGuard{GuardApprovalTimeout}, false},
		{[]NodeState{NodeReady}, []NodeState{NodePending}, owners, CauseRevoke, []EdgeGuard{GuardOwner, GuardUnclaimed}, false},
		{[]NodeState{NodeReady}, []NodeState{NodeClaimed}, []ActorKind{ActorHolder, ActorRunHost}, CauseAward, []EdgeGuard{GuardAward, GuardUnclaimed}, false},
		{[]NodeState{NodePending, NodeReady}, []NodeState{NodeFailed}, []ActorKind{ActorReaper}, CauseQueueTimeout, []EdgeGuard{GuardQueueDeadline, GuardUnclaimed}, false},
		{[]NodeState{NodePending, NodeReady, NodeClaimed}, terminal, []ActorKind{ActorRunHost}, CauseMaterialize, []EdgeGuard{GuardOwner, GuardMaterialize}, false},
		{[]NodeState{NodeClaimed}, []NodeState{NodeRunning}, []ActorKind{ActorHolder}, CauseStart, []EdgeGuard{GuardHolder}, false},
		{[]NodeState{NodeClaimed}, []NodeState{NodePending}, []ActorKind{ActorHolder}, CauseRelease, []EdgeGuard{GuardHolder}, false},
		{[]NodeState{NodeClaimed}, []NodeState{NodePending}, []ActorKind{ActorReaper}, CauseLeaseLapse, []EdgeGuard{GuardExpired}, false},
		{[]NodeState{NodeClaimed}, terminal, []ActorKind{ActorHolder}, CauseReport, []EdgeGuard{GuardHolder}, false},
		{[]NodeState{NodeRunning}, []NodeState{NodeRunning}, []ActorKind{ActorHolder}, CauseRetry, []EdgeGuard{GuardHolder, GuardRetry}, false},
		{[]NodeState{NodeRunning}, terminal, []ActorKind{ActorHolder}, CauseReport, []EdgeGuard{GuardHolder}, false},
		{[]NodeState{NodeRunning}, []NodeState{NodePending}, []ActorKind{ActorReaper}, CauseLeaseLapse, []EdgeGuard{GuardExpired, GuardDisruptions}, false},
		{[]NodeState{NodeRunning}, []NodeState{NodeFailed}, []ActorKind{ActorReaper}, CauseLeaseLapse, []EdgeGuard{GuardDisruptionCap}, false},
		{[]NodeState{NodeClaimed, NodeRunning}, []NodeState{NodeCancelled}, []ActorKind{ActorReaper}, CauseLeaseLapse, []EdgeGuard{GuardCancelLapse}, false},
		{[]NodeState{NodePending, NodeReady, NodeApprovalPending}, []NodeState{NodeCancelled}, cancellers, CauseCancel, []EdgeGuard{GuardCancel, GuardUnclaimed}, false},
		{[]NodeState{NodeClaimed, NodeRunning}, []NodeState{NodeCancelled}, cancellers, CauseCancel, []EdgeGuard{GuardCancel}, true},
	}
}

// NodeResult references authorized records; raw digests alone never authorize output attachment.
type NodeResult struct {
	Outcome        NodeState
	Reason         string
	MaskedError    string
	ExitCode       *int
	OutputRecord   *blob.RecordKey
	ManifestRecord *blob.RecordKey
}

// MaterializeSource preserves the read edge that permits copying another result.
type MaterializeSource struct {
	Kind   SourcePolicy
	Source runspec.AttemptIdentity
	Record blob.RecordKey
	Result *MaterializedResultSource
}

// MaterializedResultSource names a committed terminal result by state sequence,
// independently of earlier execution attempts. It is exclusive with Source.
type MaterializedResultSource struct {
	Ref      NodeRef
	StateSeq int64
}

// TransitionExtra carries only the columns owned by a particular edge; mismatched fields are refused.
type TransitionExtra struct {
	Result      *NodeResult
	Materialize *MaterializeSource
	Approval    *ApprovalResolution
	Claim       *ClaimAward
	Attempt     *NodeAttempt
	RetryAt     *time.Time
}

// ApprovalResolution records the resolver and policy result in the same transaction as the node edge.
type ApprovalResolution struct {
	ApprovalID string
	Decision   string
	Comment    string
}

// ClaimAward identifies an award already derived by the store; callers cannot choose its team or grants.
type ClaimAward struct {
	CapabilityPrefix string
	Holder           string
	Generation       int64
	LeaseExpiresAt   time.Time
}

// NodeTransition has no caller-selected event kind or clock; the edge determines both.
type NodeTransition struct {
	Ref   NodeRef
	To    NodeState
	Actor Actor
	Cause TransitionCause
	Extra TransitionExtra
}

// Applied distinguishes an identical replay from a state change or a cooperative cancellation request.
type Applied struct {
	Replayed        bool
	CancelRequested bool
	StateSeq        int64
	Generation      int64
	Attempt         int64
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
	Actor  ActorKind
	Reason IllegalTransitionReason
}

// Error supplies a stable reason without exposing a capability or result payload.
func (e *ErrIllegalTransition) Error() string {
	return fmt.Sprintf("illegal job transition %s -> %s by %s: %s", e.From, e.To, e.Actor, e.Reason)
}

// TransitionNode changes state, attempts, events, expansion barriers, holder release and settlement atomically.
func (t *Tenant) TransitionNode(ctx context.Context, change NodeTransition) (Applied, error) {
	var out Applied
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		out, err = transitionNodeTx(ctx, tx, t.team, change, tx.now())
		return err
	})
	return out, err
}

// safety: an expired claim is still claimed or running until the reaper commits an edge; readers cannot release it.
func nodeState(row NodeRow) (NodeState, error) {
	switch row.Status {
	case StatusDone:
		if !slices.Contains([]NodeState{NodeSuccess, NodeFailed, NodeCancelled, NodeSkipped, NodeSuperseded, NodeMemoized}, row.Outcome) || row.ClaimedBy != "" || row.LeaseExpiresAt != nil || row.CapabilityPrefix != "" || row.OwnerCapabilityPrefix != "" || row.FinishedAt == nil {
			return "", errors.New("invalid terminal job state")
		}
		return row.Outcome, nil
	case StatusRunning:
		if row.ClaimedBy == "" || row.LeaseExpiresAt == nil || row.CapabilityPrefix == "" || row.Generation <= 0 || row.Attempt <= 0 {
			return "", errors.New("running job has no claim or attempt")
		}
		return NodeRunning, nil
	case StatusApprovalPending:
		if row.ClaimedBy != "" || row.Outcome != "" {
			return "", errors.New("invalid approval state")
		}
		return NodeApprovalPending, nil
	case StatusPending:
		if row.Outcome != "" || row.FinishedAt != nil {
			return "", errors.New("invalid pending job")
		}
		if row.ClaimedBy != "" {
			if row.LeaseExpiresAt == nil || row.CapabilityPrefix == "" || row.Generation <= 0 {
				return "", errors.New("incomplete job claim")
			}
			return NodeClaimed, nil
		}
		if row.LeaseExpiresAt != nil || row.CapabilityPrefix != "" || row.OwnerCapabilityPrefix != "" {
			return "", errors.New("orphan job claim fields")
		}
		if row.ReadyAt != nil {
			return NodeReady, nil
		}
		return NodePending, nil
	default:
		return "", errors.New("unknown job status")
	}
}

// safety: holder rights and replay digest are rechecked under locks using the store clock before any status write.
func transitionNodeStateTx(ctx context.Context, tx *storeTx, team Team, change NodeTransition, now time.Time) (Applied, error) {
	if err := prepareStateActorTx(ctx, tx, team, change.Ref.RunID, change.Actor, now); err != nil {
		return Applied{}, err
	}
	if change.Cause == CauseMaterialize {
		var err error
		change, err = materializeChangeTx(ctx, tx, team, change)
		if err != nil {
			return Applied{}, err
		}
	}
	if err := lockStateRunTx(ctx, tx, team, change.Ref.RunID); err != nil {
		return Applied{}, err
	}
	now = tx.now()
	row, err := readNodeTx(ctx, tx, team, change.Ref)
	if err != nil {
		return Applied{}, err
	}
	from, err := nodeState(row)
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
		return Applied{Replayed: true, CancelRequested: row.CancelRequestedAt != nil && row.Status != StatusDone, StateSeq: row.StateSeq, Generation: row.Generation, Attempt: row.Attempt}, nil
	}
	refusal := func(reason IllegalTransitionReason) (Applied, error) {
		return Applied{}, &ErrIllegalTransition{From: string(from), To: string(change.To), Actor: change.Actor.Kind, Reason: reason}
	}
	if row.Status == StatusDone {
		return refusal(ReasonTerminal)
	}
	var edge *NodeEdge
	for _, candidate := range NodeTransitions() {
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
		return Applied{}, err
	}
	if err := attachNodeResultRecordsTx(ctx, tx, team, row, change); err != nil {
		return Applied{}, err
	}
	if err := applyNodeEdgeTx(ctx, tx, team, &row, change, *edge, now); err != nil {
		return Applied{}, err
	}
	row.StateSeq++
	row.StateDigest = digest
	row.StateActor = change.Actor
	if err := writeNodeStateTx(ctx, tx, team, row); err != nil {
		return Applied{}, err
	}
	if result := change.Extra.Result; result != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE nodes SET error=?,failure_reason=?,exit_code=? WHERE team=? AND run_id=? AND node_id=?`, result.MaskedError, result.Reason, result.ExitCode, team, row.Ref.RunID, row.Ref.NodeID); err != nil {
			return Applied{}, err
		}
	}
	kind := ""
	for _, event := range NodeEvents() {
		if event.To == change.To && event.RequestOnly == edge.RequestOnly && (event.Cause == "" || event.Cause == change.Cause) {
			kind = event.Kind
			break
		}
	}
	if kind == "" {
		return Applied{}, errors.New("job transition has no event")
	}
	if err := appendStateEventTx(ctx, tx, team, change.Ref, kind, row.StateSeq, change.Extra.Result, now); err != nil {
		return Applied{}, err
	}
	return Applied{CancelRequested: edge.RequestOnly, StateSeq: row.StateSeq, Generation: row.Generation, Attempt: row.Attempt}, nil
}

// safety: plan and expansion insertion share state_seq=0 and reject a conflicting spec hash.
func insertNodeTx(ctx context.Context, tx *storeTx, team Team, row NodeRow) error {
	if row.Ref.RunID == "" || row.Ref.NodeID == "" || row.Status != StatusPending || row.StateSeq != 0 || row.ClaimedBy != "" || row.Generation != 0 || row.SpecHash == "" {
		return errors.New("invalid initial job")
	}
	execution, err := json.Marshal(row.Execution)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,spec_hash,execution_json,expansion,seq) VALUES (?,?,?,'pending',?,?,?,?,(SELECT COALESCE(MAX(seq),-1)+1 FROM nodes WHERE team=? AND run_id=?))`, team, row.Ref.RunID, row.Ref.NodeID, []byte(row.Spec), row.SpecHash, execution, row.Expansion, team, row.Ref.RunID)
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

func guardNodeEdgeTx(ctx context.Context, tx *storeTx, team Team, row NodeRow, change NodeTransition, edge NodeEdge, now time.Time) error {
	if change.Extra.Result != nil {
		if !slices.Contains([]NodeState{NodeSuccess, NodeFailed, NodeCancelled, NodeSkipped, NodeSuperseded, NodeMemoized}, change.Extra.Result.Outcome) {
			return errors.New("result requires terminal outcome")
		}
		if change.Extra.Result.Outcome != change.To && change.Cause != CauseRetry {
			return errors.New("result does not match transition")
		}
		if int64(len(change.Extra.Result.MaskedError)) > limits.ErrorBytes {
			return errors.New("job error exceeds limit")
		}
	}
	if change.Extra.Materialize != nil && change.Cause != CauseMaterialize {
		return errors.New("unexpected materialization")
	}
	if change.Extra.Approval != nil && change.Cause != CauseApprovalResolve && change.Cause != CauseApprovalTimeout {
		return errors.New("unexpected approval")
	}
	if change.Extra.Claim != nil && change.Cause != CauseAward {
		return errors.New("unexpected claim")
	}
	if change.Extra.Attempt != nil && change.Cause != CauseStart {
		return errors.New("unexpected attempt")
	}
	if change.Extra.RetryAt != nil && change.Cause != CauseRetry {
		return errors.New("unexpected retry time")
	}
	for _, guard := range edge.Guards {
		switch guard {
		case GuardOwner:
			if err := checkOwnerTx(ctx, tx, team, change.Actor, row.Ref.RunID, now); err != nil {
				return err
			}
			if change.To == NodeReady {
				run, err := readRunTx(ctx, tx, team, row.Ref.RunID)
				if err != nil {
					return err
				}
				if runGuardRefusal(run) != "" {
					return errors.New("guard refused job release")
				}
			}
		case GuardUnclaimed:
			if row.ClaimedBy != "" {
				return holderRefusal()
			}
		case GuardHolder:
			if err := checkHolderTx(ctx, tx, team, row, change.Actor, now); err != nil {
				return err
			}
		case GuardCancel:
			if err := checkCancelActorTx(ctx, tx, team, change.Actor, row.Ref.RunID, now); err != nil {
				return err
			}
		case GuardExpired, GuardDisruptions, GuardCancelLapse, GuardDisruptionCap:
			if row.LeaseExpiresAt == nil || row.LeaseExpiresAt.After(now) {
				return holderRefusal()
			}
			cancel := row.CancelRequestedAt != nil
			run, err := readRunTx(ctx, tx, team, row.Ref.RunID)
			if err != nil {
				return err
			}
			cancel = cancel || run.CancelRequestedAt != nil
			if (guard == GuardCancelLapse) != cancel {
				return holderRefusal()
			}
			if guard == GuardDisruptions && row.Disruptions+1 >= limits.MaxDisruptions {
				return errors.New("job disruption limit reached")
			}
			if guard == GuardDisruptionCap && row.Disruptions+1 < limits.MaxDisruptions {
				return errors.New("job disruption limit not reached")
			}
		case GuardRetry:
			if change.Extra.Result == nil || change.Extra.Result.Outcome != NodeFailed || row.ApplicationRetries >= row.Execution.Retry.Value || row.CancelRequestedAt != nil {
				return errors.New("job retry budget exhausted or invalid retry")
			}
		case GuardAward:
			award := change.Extra.Claim
			if award == nil || award.Holder == "" || award.CapabilityPrefix == "" || award.Generation != row.Generation+1 || !award.LeaseExpiresAt.After(now) {
				return holderRefusal()
			}
			cap, err := loadCapabilityTx(ctx, tx, award.CapabilityPrefix)
			if err != nil {
				return err
			}
			if change.Actor.CapabilityPrefix != award.CapabilityPrefix || change.Actor.Holder != award.Holder || change.Actor.Generation != award.Generation {
				return holderRefusal()
			}
			if cap.Team != string(team) || cap.Binding.RunID != row.Ref.RunID || cap.Binding.NodeID != row.Ref.NodeID || cap.Binding.Generation != award.Generation || cap.Binding.Holder != award.Holder {
				return holderRefusal()
			}
		case GuardQueueDeadline:
			run, err := readRunTx(ctx, tx, team, row.Ref.RunID)
			if err != nil {
				return err
			}
			wait := runClaimWait(run)
			if !wait.Set || wait.Value <= 0 || row.QueueStartedAt == nil || row.QueueStartedAt.Add(wait.Value).After(now) {
				return errors.New("queue deadline not reached")
			}
		case GuardApproval, GuardApprovalTimeout:
			return checkApprovalEdgeTx(ctx, tx, team, row, change, now)
		case GuardMaterialize:
			if change.Extra.Materialize == nil || change.Extra.Result == nil {
				return errors.New("materialization requires transactional source read authority")
			}
		case GuardPublication:
			if err := checkPublisherTx(ctx, tx, team, change.Actor, row.Ref.RunID); err != nil {
				return err
			}
		default:
			return errors.New("unsupported transition guard")
		}
	}
	return nil
}

func applyNodeEdgeTx(ctx context.Context, tx *storeTx, team Team, row *NodeRow, change NodeTransition, edge NodeEdge, now time.Time) error {
	if edge.RequestOnly {
		if row.CancelRequestedAt == nil {
			row.CancelRequestedAt = &now
		}
		return nil
	}
	if row.Status == StatusRunning && (change.To != NodeRunning || change.Cause == CauseRetry) {
		outcome, reason := change.To, ""
		if change.Extra.Result != nil {
			outcome, reason = change.Extra.Result.Outcome, change.Extra.Result.Reason
		}
		if change.Cause == CauseLeaseLapse {
			outcome, reason = NodeFailed, FailureAgentLost
			if change.To == NodeCancelled {
				outcome, reason = NodeCancelled, "cancelled"
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE node_attempts SET finished_at=?,outcome=?,failure_reason=? WHERE team=? AND run_id=? AND node_id=? AND attempt=? AND generation=? AND finished_at IS NULL`, now.UnixNano(), outcome, reason, team, row.Ref.RunID, row.Ref.NodeID, row.Attempt, row.Generation)
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
	}
	switch change.To {
	case NodeReady:
		row.ReadyAt = &now
		if row.QueueStartedAt == nil {
			row.QueueStartedAt = &now
		}
	case NodeClaimed:
		award := change.Extra.Claim
		row.ClaimedBy = award.Holder
		row.CapabilityPrefix = award.CapabilityPrefix
		row.Generation = award.Generation
		row.LeaseExpiresAt = &award.LeaseExpiresAt
	case NodeRunning:
		row.Status = StatusRunning
		row.QueueStartedAt = nil
		row.Attempt++
		row.StartedAt = &now
		kind, name, location := "runner", row.ClaimedBy, "unknown"
		if a := change.Extra.Attempt; a != nil {
			if a.Identity.Team != string(team) || a.Identity.RunID != row.Ref.RunID || a.Identity.NodeID != row.Ref.NodeID || a.Identity.Attempt != row.Attempt || a.Identity.Generation != row.Generation {
				return errors.New("attempt identity does not match claim")
			}
			kind, name, location = a.ExecutorKind, a.ExecutorName, a.ExecutorLocation
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, team, row.Ref.RunID, row.Ref.NodeID, row.Attempt, row.Generation, row.ClaimedBy, row.CapabilityPrefix, kind, name, location, now.UnixNano())
		if err != nil {
			return err
		}
	case NodeApprovalPending:
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO approvals(team,run_id,node_id,requested_at,message,timeout_ms,on_timeout) VALUES (?,?,?,?,?,?,?)`, team, row.Ref.RunID, row.Ref.NodeID, now.UnixNano(), job.Approval.Message, timeout, policy); err != nil {
			return err
		}
		row.Status = StatusApprovalPending
	case NodePending:
		if row.Status == StatusRunning && change.Cause == CauseLeaseLapse {
			row.Disruptions++
		}
		row.Status = StatusPending
		row.StartedAt = nil
		clearNodeClaim(row)
	default:
		if change.Cause == CauseLeaseLapse && row.Status == StatusRunning && change.To == NodeFailed {
			row.Disruptions++
		}
		row.Status = StatusDone
		row.Outcome = change.To
		row.FinishedAt = &now
		row.QueueStartedAt = nil
		clearNodeClaim(row)
		if row.Expansion == ExpansionExpected && change.To != NodeSuccess && change.To != NodeMemoized {
			row.Expansion = ExpansionNone
		}
		if err := releaseCapacityTx(ctx, tx, team, row.Ref.RunID, row.Ref.NodeID); err != nil {
			return err
		}
	}
	return nil
}

func clearNodeClaim(row *NodeRow) {
	row.ClaimedBy = ""
	row.CapabilityPrefix = ""
	row.OwnerCapabilityPrefix = ""
	row.LeaseExpiresAt = nil
	row.ReadyAt = nil
}

func writeNodeStateTx(ctx context.Context, tx *storeTx, team Team, row NodeRow) error {
	actor, err := json.Marshal(row.StateActor)
	if err != nil {
		return err
	}
	var holder any
	if row.ClaimedBy != "" {
		holder = row.ClaimedBy
	}
	result, err := tx.ExecContext(ctx, `UPDATE nodes SET status=?,outcome=?,claimed_by=?,capability_prefix=?,owner_capability_prefix=?,generation=?,attempt=?,disruptions=?,ready_at=?,queue_started_at=?,lease_expires_at=?,started_at=?,finished_at=?,cancel_requested_at=?,state_seq=?,state_digest=?,state_actor=?,expansion=? WHERE team=? AND run_id=? AND node_id=? AND state_seq=?`, row.Status, row.Outcome, holder, row.CapabilityPrefix, row.OwnerCapabilityPrefix, row.Generation, row.Attempt, row.Disruptions, timeNS(row.ReadyAt), timeNS(row.QueueStartedAt), timeNS(row.LeaseExpiresAt), timeNS(row.StartedAt), timeNS(row.FinishedAt), timeNS(row.CancelRequestedAt), row.StateSeq, row.StateDigest, actor, row.Expansion, team, row.Ref.RunID, row.Ref.NodeID, row.StateSeq-1)
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

func appendStateEventTx(ctx context.Context, tx *storeTx, team Team, ref NodeRef, kind string, stateSeq int64, payload any, now time.Time) error {
	seq, err := appendEventTx(ctx, tx, team, ref.RunID, ref.NodeID, kind, payload, now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE events SET state_seq=? WHERE team=? AND run_id=? AND seq=?`, stateSeq, team, ref.RunID, seq)
	return err
}

func checkApprovalEdgeTx(ctx context.Context, tx *storeTx, team Team, row NodeRow, change NodeTransition, now time.Time) error {
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
	if change.Cause == CauseApprovalTimeout {
		if timeout <= 0 || time.Unix(0, requested).Add(time.Duration(timeout)*time.Millisecond).After(now) {
			return errors.New("approval deadline not reached")
		}
		if change.Actor.Kind != ActorReaper {
			if err := checkOwnerTx(ctx, tx, team, change.Actor, row.Ref.RunID, now); err != nil {
				return err
			}
		}
		if (policy == "approve") != (change.To == NodeSuccess) {
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
		resolution = change.Extra.Approval.Decision
		if (resolution != "approved" && resolution != "denied") || (resolution == "approved") != (change.To == NodeSuccess) {
			return errors.New("approval decision mismatch")
		}
	}
	comment := ""
	if change.Extra.Approval != nil {
		comment = change.Extra.Approval.Comment
	}
	approver := change.Actor.Authority.ID
	if change.Actor.Kind == ActorReaper {
		approver = change.Actor.Owner.Holder
	}
	_, err = tx.ExecContext(ctx, `UPDATE approvals SET resolved_at=?,resolution=?,approver=?,comment=? WHERE team=? AND run_id=? AND node_id=? AND resolved_at IS NULL`, now.UnixNano(), resolution, approver, comment, team, row.Ref.RunID, row.Ref.NodeID)
	return err
}
