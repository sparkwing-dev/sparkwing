package store

import (
	"context"
	"encoding/json"
	"time"

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
	Ref                NodeRef
	Status             NodeStatus
	Outcome            NodeState
	StatusDetail       string
	ClaimedBy          string
	CapabilityPrefix   string
	Generation         int64
	Attempt            int64
	ApplicationRetries int
	Disruptions        int
	ReadyAt            *time.Time
	LeaseExpiresAt     *time.Time
	StartedAt          *time.Time
	FinishedAt         *time.Time
	CancelRequestedAt  *time.Time
	StateSeq           int64
	StateDigest        string
	StateActor         Actor
	Expansion          ExpansionState
	ExpansionDigest    string
	Execution          runspec.JobExecutionSettings
	Spec               json.RawMessage
	SpecHash           string
}

// NodeAttempt preserves the node-wide ordinal across claims; generation is an attribute of that ordinal.
// A node starts at attempt zero. CauseStart allocates one on first execution and
// the next ordinal after a started disruption. CauseRetry allocates the next ordinal
// under the same claim. An unstarted release consumes no ordinal. Applied.Attempt is authoritative.
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
		{[]NodeState{NodeReady}, []NodeState{NodeFailed}, []ActorKind{ActorReaper}, CauseQueueTimeout, []EdgeGuard{GuardQueueDeadline, GuardUnclaimed}, false},
		{[]NodeState{NodePending, NodeReady}, terminal, []ActorKind{ActorRunHost}, CauseMaterialize, []EdgeGuard{GuardOwner, GuardMaterialize}, false},
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
		{[]NodeState{NodePending, NodeReady, NodeApprovalPending}, []NodeState{NodeCancelled}, []ActorKind{ActorRunHost}, CauseCancel, []EdgeGuard{GuardOwner, GuardCancel, GuardUnclaimed}, false},
		{[]NodeState{NodeClaimed, NodeRunning}, []NodeState{NodeCancelled}, []ActorKind{ActorRunHost}, CauseCancel, []EdgeGuard{GuardOwner, GuardCancel}, true},
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
func (e *ErrIllegalTransition) Error() string { panic("unimplemented: c1-data") }

// TransitionNode changes state, attempts, events, expansion barriers, holder release and settlement atomically.
func (t *Tenant) TransitionNode(ctx context.Context, change NodeTransition) (Applied, error) {
	panic("unimplemented: c1-data")
}

// safety: an expired claim is still claimed or running until the reaper commits an edge; readers cannot release it.
func nodeState(row NodeRow) (NodeState, error) { panic("unimplemented: c1-data") }

// safety: holder rights and replay digest are rechecked under locks using the store clock before any status write.
func transitionNodeTx(ctx context.Context, tx *storeTx, team Team, change NodeTransition, now time.Time) (Applied, error) {
	panic("unimplemented: c1-data")
}

// safety: plan and expansion insertion share state_seq=0 and reject a conflicting spec hash.
func insertNodeTx(ctx context.Context, tx *storeTx, team Team, row NodeRow) error {
	panic("unimplemented: c1-data")
}
