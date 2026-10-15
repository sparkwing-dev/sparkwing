package acceptance

import (
	"context"
	"fmt"
)

// StoreDataBoundaryScenarios identifies SQLite persistence proofs. Native targets
// still require their placement driver, PostgreSQL and authenticated routes.
func StoreDataBoundaryScenarios() []string {
	return []string{"storage-cas-integrity", "storage-hasmany-read-set", "storage-hasmany-large-budget", "storage-reachability-and-retention-tails", "storage-memo-and-depcache-scope", "storage-reference-edge-laundering"}
}

// CheckStoreDataBoundary checks persisted storage boundaries under a caller-owned root.
func CheckStoreDataBoundary(ctx context.Context, root, id string) (err error) {
	if id == "storage-cas-integrity" {
		return checkStoreCASBoundary(ctx, root)
	}
	if id == "storage-hasmany-large-budget" {
		return checkStoreLargePresenceBoundary(ctx, root)
	}
	if id == "storage-reference-edge-laundering" {
		return checkStoreReferenceBoundary(ctx, root)
	}
	if id == "storage-memo-and-depcache-scope" {
		return checkStoreCacheScopeBoundary(ctx, root)
	}
	if id == "storage-reachability-and-retention-tails" {
		return checkStoreReachabilityBoundary(ctx, root)
	}
	if id == "storage-hasmany-read-set" {
		return checkStorePresenceBoundary(ctx, root)
	}
	return fmt.Errorf("no store data boundary proof for %q", id)
}
