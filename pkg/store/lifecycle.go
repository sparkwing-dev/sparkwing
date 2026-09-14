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

// safety: a trigger whose claimant released it is queued work again, so the run
// it names is waiting for the next claimant rather than orphaned, and the sweeps
// that end orphaned work must leave it to the queue deadline.
func triggerRequeuedSQL(alias string) string {
	return alias + "status = '" + triggerStatusPending + "' AND " + alias + "claim_seq > 0"
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

// IsFinished reports whether this trigger has reached the state it never
// leaves, so what is keyed to it can be reclaimed.
func (t Trigger) IsFinished() bool {
	return t.Status == triggerStatusDone
}
