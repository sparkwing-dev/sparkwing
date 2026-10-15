package acceptance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkStoreDownloadBoundary(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "downloads.db")
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
	team := store.Team("download-team")
	if err := first.AsOperator().CreateTeam(ctx, team); err != nil {
		return err
	}
	tenants := make([]*store.Tenant, 2)
	for i, client := range []*store.Store{first, second} {
		tenants[i], err = client.ForTeam(ctx, team)
		if err != nil {
			return err
		}
	}
	request := store.DownloadCharge{ReplicaID: "first", Bytes: 90, FreeCapBytes: 100}
	if _, err := tenants[0].ChargeDownload(ctx, request); err != nil {
		return err
	}
	start, results := make(chan struct{}), make(chan error, 2)
	for i, tenant := range tenants {
		go func() {
			<-start
			_, err := tenant.ChargeDownload(ctx, store.DownloadCharge{ReplicaID: fmt.Sprint(i), Bytes: 10, FreeCapBytes: 100})
			results <- err
		}()
	}
	close(start)
	winners := 0
	var raceErrors error
	for range tenants {
		if err := <-results; err == nil {
			winners++
		} else if !errors.Is(err, store.ErrDownloadCap) {
			raceErrors = errors.Join(raceErrors, err)
		}
	}
	if raceErrors != nil || winners != 1 {
		return errors.Join(raceErrors, fmt.Errorf("download race had %d winners, want one", winners))
	}
	if err := second.Close(); err != nil {
		return err
	}
	second, err = store.Open(path)
	if err != nil {
		return err
	}
	tenant, err := second.ForTeam(ctx, team)
	if err != nil {
		return err
	}
	request.Bytes = 0
	if _, err := tenant.ChargeDownload(ctx, request); !errors.Is(err, store.ErrDownloadCap) {
		return fmt.Errorf("restart lost download cap: %v", err)
	}
	var total int64
	if err := second.DB().QueryRowContext(ctx, "SELECT SUM(bytes) FROM team_download_day WHERE team=?", team).Scan(&total); err != nil {
		return err
	}
	if total != 100 {
		return fmt.Errorf("replica downloads=%d, want 100", total)
	}
	return nil
}
