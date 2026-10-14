package store

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
	panic("unimplemented: c3-protocol-sdk")
}

// Scheduling projects one consistent store read into the shared decider input.
// Declarations and ordering come from accepted plan/spec bytes and expansion bodies.
// Generator IDs come from persisted ClosureSpec.Generate, and membership comes
// from accepted generated JobSpec.Groups. Every declared generator is retained,
// including accepted generators with no members. Missing or inconsistent
// declarations fail projection instead of synthesizing topology from live jobs.
// State comes only from persisted rows; elapsed leases never imply a requeue.
// Both run hosts and controller settlement call Decide on this projection.
func (snapshot RunSnapshot) Scheduling() (ScheduleSnapshot, error) {
	panic("unimplemented: c1-data")
}
