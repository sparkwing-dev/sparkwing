package acceptance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// StoreDataBoundaryScenarios identifies SQLite persistence proofs. Native targets
// still require their placement driver, PostgreSQL and authenticated routes.
func StoreDataBoundaryScenarios() []string {
	return []string{"storage-cas-integrity", "storage-replica-safe-accounting", "storage-quota-settlement-crash-replay", "storage-log-segments-and-fallback", "storage-hasmany-read-set", "storage-hasmany-large-budget", "storage-reachability-and-retention-tails", "storage-memo-and-depcache-scope", "storage-reference-edge-laundering", "storage-publish-one-way"}
}

// CheckStoreDataBoundary checks accounting with independent clients and process restarts.
// The caller owns root and its cleanup. No fixture uses the user's configuration.
func CheckStoreDataBoundary(ctx context.Context, root, id string, crash StoreUploadCrashDriver) (err error) {
	if id == "storage-cas-integrity" {
		return checkStoreCASBoundary(ctx, root)
	}
	if id == "storage-hasmany-large-budget" {
		return checkStoreLargePresenceBoundary(ctx, root)
	}
	if id == "storage-publish-one-way" {
		return checkStorePublishedLogBoundary(ctx, root)
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
	if id == "storage-quota-settlement-crash-replay" {
		return checkStoreUploadBoundary(ctx, root, crash)
	}
	if id == "storage-log-segments-and-fallback" {
		return checkStoreLogBoundary(ctx, root)
	}
	if id != "storage-replica-safe-accounting" {
		return fmt.Errorf("no store data boundary proof for %q", id)
	}
	path := filepath.Join(root, "store-data.db")
	first, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, first.Close()) }()
	second, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, second.Close()) }()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	clients := []*store.Store{first, second}
	tenants := make([]*store.Tenant, 2)
	for i, client := range clients {
		tenants[i], err = client.ForTeam(ctx, store.DefaultTeam)
		if err != nil {
			return err
		}
		for _, counter := range []string{"egress_bytes", "download_bytes", "storage_bytes", "storage_objects", "runs"} {
			if err := tenants[i].AddUsage(ctx, store.UsageDelta{Period: "fixture-period", ReplicaID: []string{"a", "b"}[i], Counter: counter, Amount: []int64{7, 11}[i]}); err != nil {
				return err
			}
		}
	}
	if err := second.Close(); err != nil {
		return err
	}
	reopened, err := store.Open(path)
	if err != nil {
		return err
	}
	second = reopened
	clients[1] = second
	tenants[1], err = second.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	for _, query := range []string{
		"SELECT SUM(bytes) FROM egress_usage WHERE team=? AND period='fixture-period'",
		"SELECT SUM(bytes) FROM team_download_day WHERE team=? AND period='fixture-period'",
		"SELECT SUM(bytes) FROM storage_month_usage WHERE team=? AND period='fixture-period'",
		"SELECT SUM(objects) FROM storage_month_usage WHERE team=? AND period='fixture-period'",
		"SELECT SUM(runs) FROM run_rate_usage WHERE team=? AND period='fixture-period' AND principal=''",
	} {
		var total int64
		if err := db.QueryRowContext(ctx, query, store.DefaultTeam).Scan(&total); err != nil {
			return err
		}
		if total != 18 {
			return fmt.Errorf("durable replica sum=%d, want 18", total)
		}
	}
	for _, tenant := range tenants {
		usage, err := tenant.StorageUsageFor(ctx, "", "fixture-period")
		if err != nil {
			return err
		}
		if usage.Team != store.DefaultTeam || usage.MonthBytes != 18 || usage.MonthObjects != 18 {
			return fmt.Errorf("public replica report=%+v, want the durable sum 18", usage)
		}
	}
	if err := tenants[0].SetStorageQuota(ctx, store.StorageQuota{MaxBytesPerMonth: 100}); err != nil {
		return err
	}
	quota, err := tenants[1].SetStorageAllowance(ctx, 50)
	if err != nil {
		return err
	}
	if quota.MaxBytesPerMonth != 100 || quota.AllowanceBytes != 50 {
		return fmt.Errorf("shared quota configuration=%+v", quota)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO quota_admission(team,quota,period,used,updated_at) VALUES (?,'storage',?,90,1)", store.DefaultTeam, time.Now().UTC().Format("2006-01")); err != nil {
		return err
	}
	start := make(chan struct{})
	type reservationResult struct {
		reservation store.QuotaReservation
		err         error
	}
	results := make(chan reservationResult, 2)
	for _, tenant := range tenants {
		go func() {
			<-start
			r, err := tenant.ReserveQuota(ctx, "storage", 8)
			results <- reservationResult{r, err}
		}()
	}
	close(start)
	winners := 0
	var reservation store.QuotaReservation
	var raceErrors error
	for range tenants {
		result := <-results
		if result.err == nil {
			winners++
			reservation = result.reservation
		} else if !errors.Is(result.err, store.ErrStorageQuota) {
			raceErrors = errors.Join(raceErrors, result.err)
		}
	}
	if raceErrors != nil {
		return raceErrors
	}
	if winners != 1 {
		return fmt.Errorf("quota race has %d winners, want one", winners)
	}
	for range 2 {
		if err := tenants[0].SettleQuota(ctx, reservation.ID, 0); err != nil {
			return err
		}
	}
	var used, reserved int64
	if err := db.QueryRowContext(ctx, "SELECT used,reserved FROM quota_admission WHERE team=? AND quota='storage'", store.DefaultTeam).Scan(&used, &reserved); err != nil {
		return err
	}
	if used != 90 || reserved != 0 {
		return fmt.Errorf("released quota=(%d,%d), want (90,0)", used, reserved)
	}
	if _, err := tenants[1].ReserveQuota(ctx, "storage", 8); err != nil {
		return fmt.Errorf("release did not restore capacity: %w", err)
	}
	return storeDataSingletonRace(ctx, clients)
}

func storeDataSingletonRace(ctx context.Context, clients []*store.Store) error {
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, client := range clients {
		go func() {
			<-start
			_, err := client.AsOperator().AcquireSingletonLease(ctx, "maintenance", []string{"a", "b"}[i], time.Hour)
			results <- err
		}()
	}
	close(start)
	winners := 0
	var raceErrors error
	for range clients {
		if err := <-results; err == nil {
			winners++
		} else if !errors.Is(err, store.ErrLockHeld) {
			raceErrors = errors.Join(raceErrors, err)
		}
	}
	if raceErrors != nil {
		return raceErrors
	}
	if winners != 1 {
		return fmt.Errorf("singleton race has %d winners, want one", winners)
	}
	return nil
}
