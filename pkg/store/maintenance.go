package store

import (
	"context"
	"time"
)

type storeMaintenanceFns struct {
	ReapExpiredTriggers           func(s *Store, ctx context.Context) ([]string, error)
	ReapTimedOutApprovals         func(s *Store, ctx context.Context) ([][2]string, error)
	FailExpiredNodeClaims         func(s *Store, ctx context.Context) ([][2]string, error)
	RecoverExpiredNodeClaims      func(s *Store, ctx context.Context) ([]AgentLossRecovery, error)
	ReapStaleConcurrencyHolders   func(s *Store, ctx context.Context) ([]ConcurrencyHolder, error)
	ReapStaleConcurrencyWaiters   func(s *Store, ctx context.Context, maxAge time.Duration) ([]ConcurrencyWaiter, error)
	SweepExpiredConcurrencyCache  func(s *Store, ctx context.Context) (int64, error)
	SweepOrphanedConcurrencyCache func(s *Store, ctx context.Context) (int64, error)
	SweepLRUConcurrencyCache      func(s *Store, ctx context.Context, keepCount int) (int64, error)
	ReconcileConcurrencyKeys      func(s *Store, ctx context.Context, lease time.Duration) (int, error)
}

// Maintenance is the in-module bridge for orchestrator-internal
// maintenance operations on Store. See [storeMaintenanceFns].
//
// External adopters should not reach for this. The supported Store
// surface is the run/node CRUD and the HTTP API in pkg/controller.
var Maintenance = storeMaintenanceFns{
	ReapExpiredTriggers:           (*Store).reapExpiredTriggers,
	ReapTimedOutApprovals:         (*Store).reapTimedOutApprovals,
	FailExpiredNodeClaims:         (*Store).failExpiredNodeClaims,
	RecoverExpiredNodeClaims:      (*Store).recoverExpiredNodeClaims,
	ReapStaleConcurrencyHolders:   (*Store).reapStaleConcurrencyHolders,
	ReapStaleConcurrencyWaiters:   (*Store).reapStaleConcurrencyWaiters,
	SweepExpiredConcurrencyCache:  (*Store).sweepExpiredConcurrencyCache,
	SweepOrphanedConcurrencyCache: (*Store).sweepOrphanedConcurrencyCache,
	SweepLRUConcurrencyCache:      (*Store).sweepLRUConcurrencyCache,
	ReconcileConcurrencyKeys:      (*Store).reconcileConcurrencyKeys,
}
