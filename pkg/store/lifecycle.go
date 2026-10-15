package store

const nodeNotDone = `status != 'done'`

func nodeRunActiveSQL(alias string) string {
	return `EXISTS (SELECT 1 FROM runs ar WHERE ar.team = ` + alias + `team
     AND ar.id = ` + alias + `run_id AND ar.finished_at IS NULL AND NOT (ar.` + runTerminalIn + `))`
}

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
	runStatusSuccess   = "success"
)

const runTerminalIn = `status IN ('success','failed','cancelled')`

const (
	outcomeSuccess    = "success"
	outcomeFailed     = "failed"
	outcomeSkipped    = "skipped"
	outcomeCancelled  = "cancelled"
	outcomeSuperseded = "superseded"
)
