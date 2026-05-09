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

// ReconcileOrphanedLocalRuns sweeps "running" rows whose latest node
// heartbeat is older than threshold and atomically transitions them
// to "failed" so subsequent reads see the truth instead of a zombie
// "running" status. Pending nodes downstream of an orphaned run get
// flipped to cancelled (matching the in-process dispatcher's
// upstream-failed cascade), so a single run-level reconciliation
// produces a fully-consistent set of node rows.
//
// Designed to be cheap enough to run lazily on every status / list
// read: it only scans rows that are status='running', and the SQL
// uses indexes already in place on (status, started_at, last_heartbeat).
// Both the CLI (runs status / runs list) and the web dashboard's HTTP
// handlers call this before reading so each surface sees the same
// reconciled state.
//
// Failures are non-fatal -- the caller's read shouldn't break because
// reconciliation hit a transient DB error. The function returns the
// count reconciled for logging / test assertions.
//
// Pass threshold=0 to use the package default (localOrphanThreshold).
func ReconcileOrphanedLocalRuns(ctx context.Context, st *store.Store, threshold time.Duration) (int, error) {
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
		// Two node transitions: "running" nodes were actively
		// executing when the orchestrator died, so they're "failed"
		// (they reached the runner, just never finished). "pending"
		// nodes never ran at all -- they were waiting on upstream
		// deps that will never complete -- so they're "cancelled"
		// with the orphan-cascade reason. This matches the
		// in-process dispatch path's "upstream-failed" semantics so
		// readers don't have to special-case orphan vs. real
		// upstream failure.
		now := time.Now().UnixNano()
		errMsg := fmt.Sprintf("orphaned: no heartbeat for >%s; orchestrator process is no longer running", threshold)
		if _, err := st.DB().ExecContext(ctx, `
UPDATE nodes
   SET status         = 'done',
       outcome        = 'failed',
       error          = ?,
       failure_reason = 'orphaned',
       finished_at    = ?
 WHERE run_id = ? AND status = 'running'`,
			errMsg, now, id); err != nil {
			return 0, err
		}
		if _, err := st.DB().ExecContext(ctx, `
UPDATE nodes
   SET status         = 'done',
       outcome        = 'cancelled',
       error          = 'orphaned: orchestrator process exited before this node ran',
       failure_reason = 'orphaned',
       finished_at    = ?
 WHERE run_id = ? AND status = 'pending'`,
			now, id); err != nil {
			return 0, err
		}
		if err := st.FinishRun(ctx, id, "failed", errMsg); err != nil {
			return 0, err
		}
	}

	// Invariant fixup: any pending node attached to an already-
	// terminal run (failed/success/cancelled) should be cancelled,
	// not left in pending. Catches leftover state from earlier
	// reconciliation passes that didn't cascade pending nodes,
	// plus any future code path that forgets the cascade. Cheap:
	// the indexed status column scopes the scan tightly.
	if _, err := st.DB().ExecContext(ctx, `
UPDATE nodes
   SET status         = 'done',
       outcome        = 'cancelled',
       error          = COALESCE(NULLIF(error, ''), 'orphaned: run terminated before this node ran'),
       failure_reason = COALESCE(NULLIF(failure_reason, ''), 'orphaned'),
       finished_at    = ?
 WHERE status = 'pending'
   AND run_id IN (SELECT id FROM runs WHERE status IN ('failed', 'success', 'cancelled'))`,
		time.Now().UnixNano()); err != nil {
		return len(orphanIDs), err
	}

	return len(orphanIDs), nil
}
