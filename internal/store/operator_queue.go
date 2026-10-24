package store

import "context"

const (
	// queueStateWaiting is an unclaimed job that is not ready.
	queueStateWaiting = "waiting"
	// QueueStateReady is a node any eligible runner may claim.
	QueueStateReady = "ready"
	// queueStateClaimed is a node a runner holds but has not started.
	queueStateClaimed = "claimed"
	// queueStateRunning is a node a runner is executing.
	queueStateRunning = "running"
	// queueStateApprovalPending is a node waiting on a human decision.
	queueStateApprovalPending = "approval_pending"
)

// QueueStates lists the persisted outstanding-job metric labels.
func QueueStates() []string {
	return []string{
		queueStateWaiting, QueueStateReady, queueStateClaimed,
		queueStateRunning, queueStateApprovalPending,
	}
}

// CountNodesByQueueState counts each unfinished job of an active run once.
func (o *Operator) CountNodesByQueueState(ctx context.Context) (map[string]int, error) {
	var waiting, ready, claimed, running, approval int
	err := o.s.queryRow(ctx,
		`SELECT
                    COALESCE(SUM(CASE WHEN status = ? AND ready_at IS NULL AND (claimed_by IS NULL OR claimed_by = '') THEN 1 ELSE 0 END), 0),
                    COALESCE(SUM(CASE WHEN status = ? AND ready_at IS NOT NULL
                                      AND (claimed_by IS NULL OR claimed_by = '') THEN 1 ELSE 0 END), 0),
                    COALESCE(SUM(CASE WHEN status = ? AND claimed_by IS NOT NULL
                                      AND claimed_by != '' THEN 1 ELSE 0 END), 0),
                    COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
                    COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0)
                   FROM nodes
                  WHERE `+nodeNotDone+` AND `+o.nodeRunActiveSQL("nodes."),
		nodeStatusPending, nodeStatusPending, nodeStatusPending,
		nodeStatusRunning, nodeStatusApprovalPending,
	).Scan(&waiting, &ready, &claimed, &running, &approval)
	if err != nil {
		return nil, err
	}
	return map[string]int{
		queueStateWaiting:         waiting,
		QueueStateReady:           ready,
		queueStateClaimed:         claimed,
		queueStateRunning:         running,
		queueStateApprovalPending: approval,
	}, nil
}
