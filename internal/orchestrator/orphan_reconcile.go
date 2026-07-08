package orchestrator

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// localOrphanThreshold is how long a "running" run can go without any
// node heartbeat before reconciliation marks it as failed. The local
// in-process heartbeat fires every 5 seconds while a node is
// executing, so 60 seconds of silence is unambiguous: either the
// orchestrator process crashed, the user Ctrl+C'd it without
// in-process cleanup, or `sparkwing` was killed mid-run. Cluster runs use
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
// The sweep itself lives in pkg/store (the shared orphan cascade also
// drives the controller-side stale-run reaper); this wrapper only
// supplies the local default threshold.
func ReconcileOrphanedLocalRuns(ctx context.Context, st *store.Store, threshold time.Duration) (int, error) {
	if threshold <= 0 {
		threshold = localOrphanThreshold
	}
	return store.Maintenance.ReconcileOrphanedLocalRuns(st, ctx, threshold)
}
