package runspec

// RunID identifies a run independently of its nodes.
type RunID string

// NodeID identifies a node within a run.
type NodeID string

// ClaimGeneration fences a claim after it has been replaced.
type ClaimGeneration int64

// AttemptOrdinal identifies an execution within a node's claim history.
type AttemptOrdinal int64

// NodeFence identifies one holder's claim on a node.
type NodeFence struct {
	RunID      RunID
	NodeID     NodeID
	Holder     string
	Generation ClaimGeneration
}

// EvalFence distinguishes an evaluator's claim from the source node's claim.
type EvalFence struct {
	RunID            RunID
	SourceNodeID     NodeID
	SourceGeneration ClaimGeneration
	Holder           string
	Generation       ClaimGeneration
}
