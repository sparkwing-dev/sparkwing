package acceptance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkStoreEventBoundary(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "events.db")
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
	request, err := seedStoreUploadBoundary(ctx, first.DB())
	if err != nil {
		return err
	}
	tenants := make([]*store.Tenant, 2)
	for i, client := range []*store.Store{first, second} {
		tenants[i], err = client.ForTeam(ctx, store.DefaultTeam)
		if err != nil {
			return err
		}
	}
	if err := tenants[0].SetStorageQuota(ctx, store.StorageQuota{MaxBytesPerRun: 10, MaxBytesPerMonth: 10}); err != nil {
		return err
	}
	ticket, err := tenants[0].ReserveBlobUpload(ctx, request)
	if err != nil {
		return err
	}
	if _, err := tenants[1].AppendEventCharged(ctx, "held", "upload", "", "note", []byte("1234")); !errors.Is(err, store.ErrStorageQuota) {
		return fmt.Errorf("event bypassed pending upload: %v", err)
	}
	if err := tenants[0].AbortBlobUpload(ctx, request.Intent.Authority, ticket.ID); err != nil {
		return err
	}
	results := make(chan error, 2)
	for i, tenant := range tenants {
		go func() {
			_, err := tenant.AppendEventCharged(ctx, fmt.Sprint(i), "upload", "", "note", []byte("ab"))
			results <- err
		}()
	}
	winners := 0
	var raceErrors error
	for range tenants {
		if err := <-results; err == nil {
			winners++
		} else if !errors.Is(err, store.ErrStorageQuota) {
			raceErrors = errors.Join(raceErrors, err)
		}
	}
	if raceErrors != nil || winners != 1 {
		return errors.Join(raceErrors, fmt.Errorf("event race had %d winners, want one", winners))
	}
	if err := second.Close(); err != nil {
		return err
	}
	second, err = store.Open(path)
	if err != nil {
		return err
	}
	tenant, err := second.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	if _, err := tenant.AppendEventCharged(ctx, "reopened", "upload", "", "note", nil); err != nil {
		return err
	}
	if _, err := tenant.AppendEventCharged(ctx, "reopened", "upload", "", "k", nil); !errors.Is(err, store.ErrStorageQuota) {
		return fmt.Errorf("restart restored spent event capacity: %v", err)
	}
	usage, err := tenant.StorageUsageFor(ctx, "upload", store.StorageMonth(time.Now()))
	if err != nil || usage.RunBytes != 10 || usage.MonthBytes != 10 || usage.RunObjects != 0 || usage.MonthObjects != 0 {
		return fmt.Errorf("event replica usage=%+v: %v", usage, err)
	}
	standing, err := tenant.StorageStanding(ctx)
	if err != nil || standing.EventBytes != 10 {
		return fmt.Errorf("event retained bytes=%+v: %v", standing, err)
	}
	return nil
}
