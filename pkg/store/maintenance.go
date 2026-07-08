package store

import (
	"context"
	"time"
)

// storeMaintenanceFns bundles the orchestrator-internal maintenance
// operations on Store. The methods are unexported on *Store so they
// don't appear in the public API surface adopters see in godoc /
// IDE autocomplete; they're surfaced here as function pointers for
// in-module callers (specifically pkg/controller's reaper goroutine).
//
// External adopters never need to call these -- they're crash-recovery
// and TTL-eviction sweeps the controller runs on a schedule. If you're
// reaching for one of these from outside this module, you almost
// certainly want a different API.
type storeMaintenanceFns struct {
	ReapExpiredTriggers          func(s *Store, ctx context.Context) ([]string, error)
	ReapStalePendingRuns         func(s *Store, ctx context.Context, grace time.Duration, reason string) ([]string, error)
	ReapStaleRunningRuns         func(s *Store, ctx context.Context, grace time.Duration, reason string) ([]string, error)
	ReapTimedOutApprovals        func(s *Store, ctx context.Context) ([][2]string, error)
	FailNodesInRun               func(s *Store, ctx context.Context, runID, errMsg, failureReason string) ([]string, error)
	FailStaleQueuedNodes         func(s *Store, ctx context.Context, olderThan time.Duration) ([][2]string, error)
	FailExpiredNodeClaims        func(s *Store, ctx context.Context) ([][2]string, error)
	ReconcileOrphanedLocalRuns   func(s *Store, ctx context.Context, threshold time.Duration) (int, error)
	ReapStaleConcurrencyHolders  func(s *Store, ctx context.Context) ([]ConcurrencyHolder, error)
	ReapStaleConcurrencyWaiters  func(s *Store, ctx context.Context, maxAge time.Duration) ([]ConcurrencyWaiter, error)
	SweepExpiredConcurrencyCache func(s *Store, ctx context.Context) (int64, error)
	SweepLRUConcurrencyCache     func(s *Store, ctx context.Context, keepCount int) (int64, error)
	ReconcileConcurrencyKeys     func(s *Store, ctx context.Context, lease time.Duration) (int, error)
}

// Maintenance is the in-module bridge for orchestrator-internal
// maintenance operations on Store. See [storeMaintenanceFns].
//
// External adopters should not reach for this. The supported Store
// surface is the run/node CRUD and the HTTP API in pkg/controller.
var Maintenance = storeMaintenanceFns{
	ReapExpiredTriggers:          (*Store).reapExpiredTriggers,
	ReapStalePendingRuns:         (*Store).reapStalePendingRuns,
	ReapStaleRunningRuns:         (*Store).reapStaleRunningRuns,
	ReapTimedOutApprovals:        (*Store).reapTimedOutApprovals,
	FailNodesInRun:               (*Store).failNodesInRun,
	FailStaleQueuedNodes:         (*Store).failStaleQueuedNodes,
	FailExpiredNodeClaims:        (*Store).failExpiredNodeClaims,
	ReconcileOrphanedLocalRuns:   (*Store).reconcileOrphanedLocalRuns,
	ReapStaleConcurrencyHolders:  (*Store).reapStaleConcurrencyHolders,
	ReapStaleConcurrencyWaiters:  (*Store).reapStaleConcurrencyWaiters,
	SweepExpiredConcurrencyCache: (*Store).sweepExpiredConcurrencyCache,
	SweepLRUConcurrencyCache:     (*Store).sweepLRUConcurrencyCache,
	ReconcileConcurrencyKeys:     (*Store).reconcileConcurrencyKeys,
}
