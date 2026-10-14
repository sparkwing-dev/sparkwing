package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// RunRow is the immutable request/spec plus the mutable lifecycle read model used by hosts.
type RunRow struct {
	ID                string
	Number            int64
	Team              Team
	Pipeline          string
	State             RunState
	Request           runspec.SealedRequest
	Lineage           runspec.Lineage
	Spec              *runspec.Spec
	Plan              json.RawMessage
	PlanHash          string
	Dispatch          string
	AllowedPools      []string
	Owner             *OwnerLeaseRef
	CancelRequestedAt *time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
	StateSeq          int64
	StateDigest       string
	StateActor        Actor
}

// AcceptedExpansionRow preserves the exact declaration bytes committed by expansion acceptance.
// Membership is reconstructed from these declarations, never from live jobs alone.
type AcceptedExpansionRow struct {
	Source           NodeRef
	SourceGeneration int64
	Body             json.RawMessage
	BodyHash         string
}

// RunSnapshot contains one consistent read, including accepted expansions in insertion order.
type RunSnapshot struct {
	Run        RunRow
	Nodes      []NodeRow
	Expansions []AcceptedExpansionRow
	StoreNow   time.Time
}

// SourcePolicy keeps resume, retry, memo and coalescing authority distinct at reconciliation.
type SourcePolicy string

// Reconciliation policies authorize a source only through the matching recorded read edge.
const (
	SourceNone        SourcePolicy = "none"
	SourceResume      SourcePolicy = "resume"
	SourceManualRetry SourcePolicy = "manual_retry"
	SourceMemo        SourcePolicy = "memo"
	SourceCoalesced   SourcePolicy = "coalesced"
)

// ReconcileKind tells a host whether it may execute; only Execute carries a newly fenced claim.
type ReconcileKind string

// Reconciliation outcomes avoid treating another holder's unfinished row as a result.
const (
	KeepTerminal ReconcileKind = "keep_terminal"
	WaitHolder   ReconcileKind = "wait_holder"
	Execute      ReconcileKind = "execute"
	Materialize  ReconcileKind = "materialize"
)

// ReconcileRequest names a possible source; the store checks it rather than trusting the host's cache hit.
type ReconcileRequest struct {
	Ref    NodeRef
	Actor  Actor
	Policy SourcePolicy
	Source *MaterializeSource
}

// ReconciledNode is decided with any self-claim in the same transaction.
type ReconciledNode struct {
	Kind   ReconcileKind
	Node   NodeRow
	Claim  *ClaimAward
	Source *MaterializeSource
}

// LeaseBeat delivers cancellation and watched jobs without changing the owner heartbeat cadence.
type LeaseBeat struct {
	ExpiresAt       time.Time
	CancelRequested bool
	CancelReason    string
	Watched         bool
	WatchedNodes    []string
}

// StateEvent uses a single per-run sequence and a unique node state sequence for lifecycle writes.
type StateEvent struct {
	Seq      int64
	StateSeq int64
	RunID    string
	NodeID   string
	Kind     string
	At       time.Time
	Payload  json.RawMessage
}

// ReadRun returns only the Tenant's run, including both seals and the stable team number.
func (t *Tenant) ReadRun(ctx context.Context, runID string) (RunRow, error) {
	panic("unimplemented: c1-data")
}

// ReadNode returns persisted state without interpreting an elapsed lease as an implicit requeue.
func (t *Tenant) ReadNode(ctx context.Context, ref NodeRef) (NodeRow, error) {
	panic("unimplemented: c1-data")
}

// ReadSnapshot prevents scheduling from a mix of states observed in different transactions.
func (t *Tenant) ReadSnapshot(ctx context.Context, runID string) (RunSnapshot, error) {
	panic("unimplemented: c1-data")
}

// ReadStateEvents resumes strictly after the caller's sequence; state transitions own lifecycle event writes.
func (t *Tenant) ReadStateEvents(ctx context.Context, runID string, after int64, limit int) ([]StateEvent, error) {
	panic("unimplemented: c1-data")
}

// ReconcileNode decides and self-claims atomically; WaitHolder callers reconcile again after requeue.
// Materialization reads outcome and authorized result edges from persisted source state,
// never from a caller assertion. Source.Kind must match Policy; SourceNone forbids Source.
func (t *Tenant) ReconcileNode(ctx context.Context, request ReconcileRequest) (ReconciledNode, error) {
	panic("unimplemented: c1-data")
}

// RenewOwner also renews self-claimed jobs and continuously live plan and job concurrency holders atomically.
func (t *Tenant) RenewOwner(ctx context.Context, actor Actor) (LeaseBeat, error) {
	panic("unimplemented: c1-data")
}

// RenewNodeClaim renews the exact live claim and its bound concurrency holders using the store clock.
func (t *Tenant) RenewNodeClaim(ctx context.Context, ref NodeRef, actor Actor) (LeaseBeat, error) {
	panic("unimplemented: c1-data")
}

// StateEventRule makes every lifecycle event name store-owned on customer surfaces.
type StateEventRule struct {
	RequestOnly bool
	Cause       TransitionCause
	To          NodeState
	Kind        string
}

// NodeEvents matches requested To and RequestOnly; an empty cause matches every cause for that terminal outcome.
// Cancellation requests emit job_cancel_requested while preserving claimed/running state until the holder drains.
func NodeEvents() []StateEventRule {
	return []StateEventRule{
		{false, CauseRelease, NodeReady, "job_ready"},
		{false, CauseRelease, NodePending, "job_requeued"},
		{false, CauseRevoke, NodePending, "job_revoked"},
		{false, CauseAward, NodeClaimed, "job_claimed"},
		{false, CauseApprovalOpen, NodeApprovalPending, "job_approval_pending"},
		{false, CauseStart, NodeRunning, "job_started"},
		{false, CauseRetry, NodeRunning, "job_retrying"},
		{false, CauseLeaseLapse, NodePending, "job_requeued"},
		{true, CauseCancel, NodeCancelled, "job_cancel_requested"},
		{false, "", NodeSuccess, "job_succeeded"},
		{false, "", NodeFailed, "job_failed"},
		{false, "", NodeCancelled, "job_cancelled"},
		{false, "", NodeSkipped, "job_skipped"},
		{false, "", NodeSuperseded, "job_superseded"},
		{false, "", NodeMemoized, "job_memoized"},
	}
}

// LockClass fixes transaction lock order across claims, state, capacity and the financial ledger.
type LockClass string

// LockOrder is acquired left to right; keys within one class use lexical order.
// Operations starting later in the list must not acquire an earlier class afterward.
func LockOrder() []LockClass {
	return []LockClass{
		"singleton_lease", "capability_ancestry", "owner_unit", "run", "node",
		"concurrency_entry", "free_slot", "quota", "blob", "credit_ledger",
		"team", "card_charge", "card_attempt",
	}
}

// safety: one advisory-lock spelling must serve all dialects to keep the declared lock order reviewable.
func advisoryLockTx(ctx context.Context, tx *storeTx, name string) error {
	panic("unimplemented: c1-data")
}
