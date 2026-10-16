package store

import "context"

// CountPendingNodes returns the count of unclaimed ready nodes.
func (o *Operator) CountPendingNodes(ctx context.Context) (int, error) {
	var n int
	err := o.s.queryRow(
		ctx,
		`SELECT COUNT(*) FROM nodes
		  WHERE ready_at IS NOT NULL AND (claimed_by IS NULL OR claimed_by = '')
		    AND status = 'pending' AND `+nodeRunActiveSQL("nodes."),
	).Scan(&n)
	return n, err
}

const (
	// QueueStateWaiting is an unclaimed job that is not ready.
	QueueStateWaiting = "waiting"
	// QueueStateReady is a node any eligible runner may claim.
	QueueStateReady = "ready"
	// QueueStateClaimed is a node a runner holds but has not started.
	QueueStateClaimed = "claimed"
	// QueueStateRunning is a node a runner is executing.
	QueueStateRunning = "running"
	// QueueStateApprovalPending is a node waiting on a human decision.
	QueueStateApprovalPending = "approval_pending"
)

// QueueStates lists the persisted outstanding-job metric labels.
func QueueStates() []string {
	return []string{
		QueueStateWaiting, QueueStateReady, QueueStateClaimed,
		QueueStateRunning, QueueStateApprovalPending,
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
                  WHERE `+nodeNotDone+` AND `+nodeRunActiveSQL("nodes."),
		nodeStatusPending, nodeStatusPending, nodeStatusPending,
		nodeStatusRunning, NodeStatusApprovalPending,
	).Scan(&waiting, &ready, &claimed, &running, &approval)
	if err != nil {
		return nil, err
	}
	return map[string]int{
		QueueStateWaiting:         waiting,
		QueueStateReady:           ready,
		QueueStateClaimed:         claimed,
		QueueStateRunning:         running,
		QueueStateApprovalPending: approval,
	}, nil
}
