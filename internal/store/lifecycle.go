package store

const nodeNotDone = `status != 'done'`

func (o *Operator) nodeRunActiveSQL(alias string) string {
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

const runTerminalIn = `status IN ('success','dry_run','failed','cancelled')`
