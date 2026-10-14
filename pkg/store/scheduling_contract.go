package store

import (
	"context"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// ScheduleNode is the immutable graph projection consumed by the shared decider.
// Status, outcome and expansion use the final schema's spellings. Claimed means
// a current holder exists; only the transition writer may judge its lease rights.
type ScheduleNode struct {
	ID              string
	Kind            string
	Status          string
	Outcome         string
	Generation      int64
	Ready           bool
	Claimed         bool
	Deps            []string
	DepsIfPresent   []string
	NeedsGenerated  []string
	OnFailureOf     string
	ContinueOnError bool
	Optional        bool
	Expansion       string
}

// ScheduleExpansion preserves generator identity even when its accepted member set is empty.
// Each declared generator has one row, with its source's persisted expansion state.
type ScheduleExpansion struct {
	GeneratorID  string
	SourceNodeID string
	State        string
	Members      []string
}

// ScheduleSnapshot is a stable read of the accepted graph and cancellation state.
// Nodes remain in accepted plan order, including persisted expansion insert order.
type ScheduleSnapshot struct {
	CancelRequested bool
	Nodes           []ScheduleNode
	Expansions      []ScheduleExpansion
}

// DecisionKind separates graph intent from the store's transition authority.
type DecisionKind string

const (
	DecisionReady        DecisionKind = "ready"
	DecisionApproval     DecisionKind = "approval_pending"
	DecisionTerminal     DecisionKind = "terminal"
	DecisionCancelHolder DecisionKind = "cancel_holder"
	DecisionExpand       DecisionKind = "expand"
)

// NodeDecision is applied through TransitionNode, never by direct status updates.
type NodeDecision struct {
	NodeID      string
	Generation  int64
	GeneratorID string
	Kind        DecisionKind
	Outcome     string
	Reason      string
}

// ScheduleDecision contains all graph work plus an optional terminal run verdict.
// A missing verdict means unsettled, including any expected expansion.
type ScheduleDecision struct {
	Nodes   []NodeDecision
	Outcome string
	Reason  string
}

// Decide is the run host's entry to the same pure decision used in store settlement.
// It performs no I/O, samples no clock and mutates no input. Controller settlement
// applies its decisions inside the transition transaction and rechecks holder rights.
func Decide(snapshot ScheduleSnapshot) ScheduleDecision {
	return decide(snapshot)
}

// safety: one pure decider keeps remote and local owners from releasing different dependency graphs.
func decide(snapshot ScheduleSnapshot) ScheduleDecision {
	panic("unimplemented: c3-protocol-sdk")
}

// ReconcileRef identifies one accepted node; team is the Tenant receiver's scope.
type ReconcileRef struct {
	RunID  string
	NodeID string
}

// ReconcileActor identifies already-authenticated owner authority, never request-body identity.
// The store rechecks this capability's binding, owner lease and holder rights in the transaction.
type ReconcileActor struct {
	CapabilityPrefix string
	Holder           string
	OwnerGeneration  int64
}

// MaterializationSource names persisted result authority for retry reuse or a memo hit.
// Existence and every blob reference edge are authorized before any terminal write.
type MaterializationSource struct {
	RunID    string
	NodeID   string
	Attempt  int64
	RecordID string
	SpecHash string
	Outcome  string
}

// SourcePolicyC3 supplies possible reuse without letting a host overwrite a live holder.
type SourcePolicyC3 struct {
	Reuse *MaterializationSource
}

// ReconcileKindC3 is the complete set of atomic reconciliation results.
type ReconcileKindC3 string

const (
	KeepTerminalC3 ReconcileKindC3 = "keep_terminal"
	WaitHolderC3   ReconcileKindC3 = "wait_holder"
	ExecuteC3      ReconcileKindC3 = "execute"
	MaterializeC3  ReconcileKindC3 = "materialize"
)

// ReconcileResult carries the generation that fences execution or event following.
// ExecuteC3 means self-claim committed. MaterializeC3 means the terminal copy committed.
type ReconcileResult struct {
	Kind       ReconcileKindC3
	Generation int64
	Attempt    int64
	StateSeq   int64
	Source     *MaterializationSource
}

// ReconcileNodeC3 chooses and self-claims or materializes atomically. Terminal rows
// stay terminal; a live foreign holder yields WaitHolderC3, never a copied result.
// After terminal or requeue events, a WaitHolderC3 caller reconciles again.
func (t *Tenant) ReconcileNodeC3(ctx context.Context, ref ReconcileRef, actor ReconcileActor, policy SourcePolicyC3) (ReconcileResult, error) {
	panic("unimplemented: c3-protocol-sdk")
}

// ExpansionSubmissionC3 binds generated declarations to their persisted source generation.
// Body contains only jobs, decoded by ValidatePlan in expansion mode against the
// sealed spec and accepted graph. It cannot change plan settings or earlier jobs.
type ExpansionSubmissionC3 struct {
	RunID            string          `json:"run_id"`
	SourceJob        string          `json:"source_job"`
	SourceGeneration int64           `json:"source_generation"`
	Body             json.RawMessage `json:"body"`
}

// ExpansionAuthority identifies the live owner delegation or awarded eval claim.
// The capability owns team, binding and ancestry. The caller cannot assert them
// through ExpansionSubmissionC3, nor reuse a completed source's work capability.
type ExpansionAuthority struct {
	CapabilityPrefix string
}

// ExpansionResult records replay and the exact accepted job specs and hashes.
type ExpansionResult struct {
	Replayed   bool
	Jobs       []nodeproto.JobSpec
	SpecHashes map[string]string
}

// AcceptExpansionC3 validates, inserts and resolves the expansion in one transaction.
// It checks owner/eval liveness, immutable source generation, expected state and
// cancellation, enforces 256 jobs per fan-out and stores specs with their hashes.
// An identical body replays even after accepted; a different body conflicts.
func (t *Tenant) AcceptExpansionC3(ctx context.Context, authority ExpansionAuthority, submission ExpansionSubmissionC3) (ExpansionResult, error) {
	panic("unimplemented: c3-protocol-sdk")
}

// FailExpansion resolves a generator error under the same authority and fence as
// AcceptExpansionC3. It marks failed and settles needs_generated as upstream_failed.
func (t *Tenant) FailExpansion(ctx context.Context, authority ExpansionAuthority, ref ExpansionSubmissionC3, message string) error {
	panic("unimplemented: c3-protocol-sdk")
}
