package store

const nodeNotDone = `status != 'done'`

func nodeClaimLiveSQL(alias string) string {
	return alias + "claimed_by IS NOT NULL AND " + alias +
		"lease_expires_at IS NOT NULL AND " + alias + "lease_expires_at > ?"
}

func triggerClaimLiveSQL(alias string) string {
	return alias + "status = 'claimed' AND " + alias +
		"lease_expires_at IS NOT NULL AND " + alias + "lease_expires_at > ?"
}

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
