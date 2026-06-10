package store

// Lifecycle vocabulary for node, trigger, and run rows. The status
// strings live in the database; every Go-side query that filters or
// writes a lifecycle state routes through these definitions so a
// transition adjusted in one claim/reap/fail path cannot be missed by
// its siblings. The schema DDL keeps one literal copy inside a
// partial-index definition; the lifecycle guard test pins the counts.

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
