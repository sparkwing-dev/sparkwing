package store

import "context"

// ExecutionActivity reports this tenant's executable runs and live resource holders.
type ExecutionActivity struct{ Runs, QueuedTriggers, HeldLeases int }

// ExecutionActivity reads one tenant's executable activity under a store-clock snapshot.
func (t *Tenant) ExecutionActivity(ctx context.Context) (ExecutionActivity, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return ExecutionActivity{}, err
	}
	defer rollbackOrLog(tx)
	now := tx.time.UnixNano()
	var activity ExecutionActivity
	err = tx.QueryRowContext(ctx, `WITH executable AS (
 SELECT id FROM runs WHERE team=? AND intake_mode='execute' AND request_json IS NOT NULL
), active AS (
 SELECT r.id FROM runs r JOIN executable e ON e.id=r.id
 WHERE r.team=? AND r.status IN ('pending','running') AND r.finished_at IS NULL
), held AS (
 SELECT t.id FROM triggers t JOIN executable e ON e.id=t.id
 WHERE t.team=? AND t.status='claimed' AND t.lease_expires_at>?
 UNION ALL
 SELECT n.run_id FROM nodes n JOIN executable e ON e.id=n.run_id
 WHERE n.team=? AND n.claimed_by IS NOT NULL AND n.status<>'done' AND n.lease_expires_at>?
 UNION ALL
 SELECT u.run_id FROM eval_units u JOIN executable e ON e.id=u.run_id
 WHERE u.team=? AND u.claimed_by IS NOT NULL AND u.status IN ('claimed','running') AND u.lease_expires_at>?
 UNION ALL
 SELECT h.run_id FROM concurrency_holders h JOIN executable e ON e.id=h.run_id
 WHERE h.team=? AND h.lease_expires_at>?
)
SELECT (SELECT COUNT(*) FROM active),
 (SELECT COUNT(*) FROM triggers t JOIN active a ON a.id=t.id WHERE t.team=? AND t.status='pending'),
 (SELECT COUNT(*) FROM held)`, t.team, t.team, t.team, now, t.team, now, t.team, now, t.team, now, t.team).Scan(&activity.Runs, &activity.QueuedTriggers, &activity.HeldLeases)
	if err != nil {
		return ExecutionActivity{}, err
	}
	return activity, tx.Commit()
}
