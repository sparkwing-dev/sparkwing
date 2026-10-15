package store

const nodeNotDone = `status != 'done'`

func nodeRunActiveSQL(alias string) string {
	return `EXISTS (SELECT 1 FROM runs ar WHERE ar.team = ` + alias + `team
     AND ar.id = ` + alias + `run_id AND ar.finished_at IS NULL AND NOT (ar.` + runTerminalIn + `))`
}

// safety: a controller-dispatched run's nodes carry a kind, and only the
// launcher's claim hands their pods the claim token they report through, so
// every other claim path passes over them.
const nodeTriggerDispatched = `kind = ''`

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
	triggerStatusFailed  = "failed"
)

const (
	runStatusPending   = "pending"
	runStatusRunning   = "running"
	runStatusFailed    = "failed"
	runStatusCancelled = "cancelled"
	runStatusSuccess   = "success"
)

const runTerminalIn = `status IN ('success','failed','cancelled')`

// IsFinished reports whether this trigger has reached the state it never
// leaves, so what is keyed to it can be reclaimed.
func (t Trigger) IsFinished() bool {
	return t.Status == triggerStatusDone || t.Status == triggerStatusFailed
}

const (
	outcomeSuccess    = "success"
	outcomeFailed     = "failed"
	outcomeSkipped    = "skipped"
	outcomeCancelled  = "cancelled"
	outcomeSuperseded = "superseded"
)
