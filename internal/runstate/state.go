// Package runstate defines execution state shared by persistence, hosts and transport.
package runstate

import (
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// CapacityBinding names durable work. Generation authenticates the current award;
// it is never part of a reservation's identity or arrival order.
type CapacityBinding struct {
	RunID      string
	NodeID     string
	Generation int64
}

// AcquireKind covers user-selectable + non-selectable outcomes.
type AcquireKind string

const (
	AcquireGranted          AcquireKind = "granted"
	AcquireQueued           AcquireKind = "queued"
	AcquireSkipped          AcquireKind = "skipped"
	AcquireFailed           AcquireKind = "failed"
	AcquireCancellingOthers AcquireKind = "cancelling_others"
)

// NodeCommandExtra omits award and execution authority.
type NodeCommandExtra struct {
	OwnerPolicy *OwnerPolicyResult        `json:"owner_policy,omitempty"`
	Result      *NodeResult               `json:"result,omitempty"`
	Materialize *MaterializeSource        `json:"materialize,omitempty"`
	Approval    *ApprovalResolution       `json:"approval,omitempty"`
	RetryAt     *time.Time                `json:"retry_at,omitempty"`
	Publication *runspec.PublishedAttempt `json:"publication,omitempty"`
}

// NodeState separates offerability and claim ownership from the persisted status.
type NodeState string

const (
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

const (
	StatusPending         NodeStatus = "pending"
	StatusRunning         NodeStatus = "running"
	StatusDone            NodeStatus = "done"
	StatusApprovalPending NodeStatus = "approval_pending"
)

// ExpansionState prevents settlement before generated jobs have been accepted.
type ExpansionState string

const (
	ExpansionAbsent   ExpansionState = ""
	ExpansionExpected ExpansionState = "expected"
	ExpansionAccepted ExpansionState = "accepted"
	ExpansionFailed   ExpansionState = "failed"
	ExpansionNone     ExpansionState = "none"
)

// ActorKind identifies which store-verified authority may take an edge.
type ActorKind string

const (
	ActorRunHost    ActorKind = "run_host"
	ActorHolder     ActorKind = "holder"
	ActorClaimant   ActorKind = "claimant"
	ActorController ActorKind = "controller"
	ActorReaper     ActorKind = "reaper"
	ActorOperator   ActorKind = "operator"
	ActorLauncher   ActorKind = "launcher"
)

// OwnerLeaseKind selects the persisted liveness row; local hosts use a trigger lease too.
type OwnerLeaseKind string

const (
	OwnerTrigger    OwnerLeaseKind = "trigger"
	OwnerController OwnerLeaseKind = "controller"
	OwnerEval       OwnerLeaseKind = "eval"
)

// OwnerLeaseRef identifies a fence to re-read under the store clock, never a caller's liveness assertion.
type OwnerLeaseRef struct {
	Kind OwnerLeaseKind
	runspec.EvalFence
}

// Actor carries server-derived tagged authority; C2 resolves it through the current
// transaction. CapabilityPrefix is a separate claim fence and never substitutes
// for a session, console or peer authority reference.
type Actor struct {
	Result           *capability.RecordedResult
	ConsoleProof     *capability.ConsoleProof
	Authority        capability.AuthorityRef
	Kind             ActorKind
	CapabilityPrefix string
	runspec.NodeFence
	Owner OwnerLeaseRef
}

// NodeRow contains the state facts consumed by the shared decision and reconciliation functions.
type NodeRow struct {
	Ref                   runspec.NodeRef
	Status                NodeStatus
	Outcome               NodeState
	StatusDetail          string
	FailureReason         string
	ClaimedBy             string
	CapabilityPrefix      string
	OwnerCapabilityPrefix string
	Generation            runspec.ClaimGeneration
	Attempt               runspec.AttemptOrdinal
	ApplicationRetries    int
	Disruptions           int
	ReadyAt               *time.Time
	QueueStartedAt        *time.Time
	LeaseExpiresAt        *time.Time
	StartedAt             *time.Time
	FinishedAt            *time.Time
	CancelRequestedAt     *time.Time
	CancelReason          string
	StateSeq              int64
	StateDigest           string
	StateActor            Actor
	Expansion             ExpansionState
	ExpansionDigest       string
	Execution             runspec.JobExecutionSettings
	Spec                  json.RawMessage
	SpecHash              string
	OutputRecord          *blob.RecordKey
	ManifestRecord        *blob.RecordKey
	OwnerExecution        *OwnerExecution `json:",omitempty"`
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

const (
	CauseRelease        TransitionCause = "release"
	CauseUnpriced       TransitionCause = "unpriced"
	CauseApprovalOpen   TransitionCause = "approval_open"
	CauseRevoke         TransitionCause = "revoke"
	CauseAward          TransitionCause = "award"
	CauseQueueTimeout   TransitionCause = "queue_timeout"
	CauseStart          TransitionCause = "start"
	CauseReport         TransitionCause = "report"
	CauseLaunchRejected TransitionCause = "launch_rejected"
	CauseRetry          TransitionCause = "retry"
	CauseLeaseLapse     TransitionCause = "lease_lapse"
	CausePublish        TransitionCause = "publish"
	CausePublishAttempt TransitionCause = "publish_attempt"
)

// NodeResult references authorized records; raw digests alone never authorize output attachment.
type NodeResult struct {
	Outcome        NodeState
	Reason         string
	MaskedError    string
	ExitCode       *int
	OutputRecord   *blob.RecordKey
	ManifestRecord *blob.RecordKey
	Failure        *nodeproto.JobFailure
}

// MaterializeSource preserves the read edge that permits copying another result.
type MaterializeSource struct {
	Kind   SourcePolicy
	Source runspec.AttemptIdentity
	Result *MaterializedResultSource
}

// MaterializedResultSource names a committed terminal result by state sequence,
// independently of earlier execution attempts. It is exclusive with Source.
type MaterializedResultSource struct {
	Ref      runspec.NodeRef
	StateSeq int64
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
	Generation       runspec.ClaimGeneration
	LeaseExpiresAt   time.Time
}

// Applied distinguishes an identical replay from a state change or a cooperative cancellation request.
type Applied struct {
	Waiting         bool
	Outcome         RunState
	Reason          string
	Replayed        bool
	CancelRequested bool
	StateSeq        int64
	Generation      runspec.ClaimGeneration
	Attempt         runspec.AttemptOrdinal
}

// OwnerExecution binds a private placement decision to its owner and declaration.
type OwnerExecution struct {
	Owner          capability.AuthorityRef
	Team           string
	Generation     int64
	Node           runspec.NodeRef
	NodeGeneration int64
	SpecHash       string
	Allowed        bool
}

// OwnerPolicyKind identifies a declared approval predicate result.
type OwnerPolicyKind string

const (
	PolicyApprovalFail   OwnerPolicyKind = "approval_fail"
	PolicyApprovalBypass OwnerPolicyKind = "approval_bypass"
)

// OwnerPolicyResult binds a declared decision to its captured work generation.
type OwnerPolicyResult struct {
	Kind       OwnerPolicyKind
	Generation runspec.ClaimGeneration
}

// Segment separates decoded byte offsets and size from the stored payload's bytes and digest.
type Segment struct {
	Index       int64
	Offset      int64
	Size        int64
	StoredBytes int64
	Digest      string
	ObjectKey   string
}

// RetrySelection records execution intent independently of the source seals.
type RetrySelection struct {
	Mode    string                              `json:"mode"`
	Results map[string]MaterializedResultSource `json:"results"`
}

// RunState is both the persisted status and the logical state; terminal states never reopen.
type RunState string

const (
	RunPending   RunState = "pending"
	RunRunning   RunState = "running"
	RunSuccess   RunState = "success"
	RunDryRun    RunState = "dry_run"
	RunFailed    RunState = "failed"
	RunCancelled RunState = "cancelled"
)

// RunCause distinguishes replay-safe lifecycle writes from trigger recovery that keeps the same run alive.
type RunCause string

const (
	RunStart          RunCause = "start"
	RunFinish         RunCause = "finish"
	RunCancel         RunCause = "cancel"
	RunQueueTimeout   RunCause = "queue_timeout"
	RunOwnerLost      RunCause = "owner_lost"
	RunPublish        RunCause = "publish"
	RunLaunchRejected RunCause = "launch_rejected"
)

// RunTransition never accepts a client clock or effect payload; those derive from committed state.
type RunTransition struct {
	RunID       runspec.RunID
	To          RunState
	Actor       Actor
	Cause       RunCause
	Reason      string
	MaskedError string
}

// RunCompletion reports completion facts; the store derives the terminal verdict.
type RunCompletion struct {
	RunID       runspec.RunID
	Actor       Actor
	Cause       RunCause
	Reason      string
	MaskedError string
}

// FinishRunRequest binds the terminal verdict to the owner unit that must end in the same transaction.
type FinishRunRequest struct {
	Transition RunCompletion
	Unit       OwnerLeaseRef
}

// AcceptedExpansionRow preserves the exact declaration bytes committed by expansion acceptance.
// Membership is reconstructed from these declarations, never from live jobs alone.
type AcceptedExpansionRow struct {
	Source           runspec.NodeRef
	SourceGeneration int64
	Body             json.RawMessage
	BodyHash         string
	Registration     runspec.SecretRegistration
}

// SourcePolicy keeps resume, retry and coalescing authority distinct at reconciliation.
// A memo hit is not a source: it settles through the executing holder's start permit.
type SourcePolicy string

const (
	SourceNone        SourcePolicy = "none"
	SourceResume      SourcePolicy = "resume"
	SourceManualRetry SourcePolicy = "manual_retry"
	SourceCoalesced   SourcePolicy = "coalesced"
)

// ReconcileKind tells a host whether it may execute; only Execute carries a newly fenced claim.
type ReconcileKind string

const (
	KeepTerminal ReconcileKind = "keep_terminal"
	WaitHolder   ReconcileKind = "wait_holder"
	Execute      ReconcileKind = "execute"
	Materialize  ReconcileKind = "materialize"
	Dispatch     ReconcileKind = "dispatch"
)

// ReconciledNode is decided with any self-claim in the same transaction.
type ReconciledNode struct {
	Kind   ReconcileKind
	Node   NodeRow
	Claim  *ClaimAward
	Source *MaterializeSource
	// Bearer is delivered only to the executing host after the self-claim commits.
	Bearer []byte `json:"-"`
}

// LeaseBeat delivers cancellation and watched jobs without changing the owner heartbeat cadence.
type LeaseBeat struct {
	ExpiresAt       time.Time
	CancelRequested bool
	CancelReason    string
	Watched         bool
	WatchedNodes    []string
}

// LogSegments is the persisted read model for one tagged subject, including an empty tail.
type LogSegments struct {
	Subject          blob.LogSubject
	Segments         []Segment
	Complete         bool
	IncompleteReason string
}
