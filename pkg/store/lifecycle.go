package store

// nodeNotDone is the canonical SQL filter for a node that has not
// reached its terminal status. Claim, revoke, reap, and fail paths all
// share it.
const nodeNotDone = `status != 'done'`

// nodeFailSet is the single definition of a node's terminal failed
// transition: a done status is never written without its outcome.
const nodeFailSet = `status = 'done', outcome = 'failed'`

// Node lifecycle statuses as written by CreateNode / StartNode /
// FinishNode.
const (
	nodeStatusPending = "pending"
	nodeStatusRunning = "running"
	nodeStatusDone    = "done"
)

// Trigger lifecycle statuses: pending -> claimed -> done, with
// claimed -> pending on lease expiry.
const (
	triggerStatusPending = "pending"
	triggerStatusClaimed = "claimed"
	triggerStatusDone    = "done"
)

// Run statuses the store itself transitions (callers stamp terminal
// statuses through FinishRun with their own outcome strings).
const (
	runStatusPending = "pending"
	runStatusRunning = "running"
	runStatusFailed  = "failed"
)

// runTerminalIn is the canonical SQL filter for a run in a terminal
// status, shared by pruning and the orphan fixup.
const runTerminalIn = `status IN ('success','failed','cancelled')`
