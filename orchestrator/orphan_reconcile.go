package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// localOrphanThreshold is how long a "running" run can go without any
// node heartbeat before reconciliation marks it as failed. The local
// in-process heartbeat fires every 5 seconds while a node is
// executing, so 60 seconds of silence is unambiguous: either the
// orchestrator process crashed, the user Ctrl+C'd it without
// in-process cleanup, or `wing` was killed mid-run. Cluster runs use
// their own controller-side lease enforcement and aren't touched
// here.
const localOrphanThreshold = 60 * time.Second

// reconcileOrphanedLocalRuns sweeps "running" rows whose latest node
// heartbeat is older than threshold and atomically transitions them
// to "failed" so subsequent reads see the truth instead of a zombie
// "running" status.
//
// Designed to be cheap enough to run lazily on every status / list
// read: it only scans rows that are status='running', and the SQL
// uses indexes already in place on (status, started_at, last_heartbeat).
//
// Failures are non-fatal -- the caller's read shouldn't break because
// reconciliation hit a transient DB error. The function returns the
// count reconciled for logging / test assertions.
func reconcileOrphanedLocalRuns(ctx context.Context, st *store.Store, threshold time.Duration) (int, error) {
	if threshold <= 0 {
		threshold = localOrphanThreshold
	}
	cutoff := time.Now().Add(-threshold).UnixNano()

	// Candidate runs: status='running' that started before the cutoff
	// (so a freshly-started run never trips this) AND whose newest
	// node heartbeat is also before the cutoff (or has no heartbeat
	// at all). MAX(last_heartbeat) is NULL when no node has ever
	// heartbeated; COALESCE pins it to the run's started_at so the
	// "no heartbeat ever" case is treated as orphaned once we cross
	// the threshold.
	rows, err := st.DB().QueryContext(ctx, `
SELECT r.id
  FROM runs r
 WHERE r.status = 'running'
   AND r.started_at < ?
   AND COALESCE(
         (SELECT MAX(last_heartbeat) FROM nodes n WHERE n.run_id = r.id),
         r.started_at
       ) < ?`,
		cutoff, cutoff)
	if err != nil {
		return 0, err
	}
	var orphanIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		orphanIDs = append(orphanIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, id := range orphanIDs {
		// Transition any "running" nodes to "failed" first so
		// node-level views match. Then close out the run row.
		// Both UPDATEs are idempotent: replaying after a partial
		// success is a no-op.
		errMsg := fmt.Sprintf("orphaned: no heartbeat for >%s; orchestrator process is no longer running", threshold)
		if _, err := st.DB().ExecContext(ctx, `
UPDATE nodes
   SET status         = 'done',
       outcome        = 'failed',
       error          = ?,
       failure_reason = 'orphaned',
       finished_at    = ?
 WHERE run_id = ? AND status = 'running'`,
			errMsg, time.Now().UnixNano(), id); err != nil {
			return 0, err
		}
		if err := st.FinishRun(ctx, id, "failed", errMsg); err != nil {
			return 0, err
		}
	}
	return len(orphanIDs), nil
}
