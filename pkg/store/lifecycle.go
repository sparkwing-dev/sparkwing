package store

const nodeNotDone = `status != 'done'`

const nodeFailSet = `status = 'done', outcome = 'failed'`

const (
	nodeStatusPending = "pending"
	nodeStatusRunning = "running"
	nodeStatusDone    = "done"
)

const (
	triggerStatusPending = "pending"
	triggerStatusClaimed = "claimed"
	triggerStatusDone    = "done"
)

const (
	runStatusPending   = "pending"
	runStatusRunning   = "running"
	runStatusFailed    = "failed"
	runStatusCancelled = "cancelled"
)

const runTerminalIn = `status IN ('success','failed','cancelled')`
