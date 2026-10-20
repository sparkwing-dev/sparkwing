package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestReplicaUsageSurvivesReopening(t *testing.T) {
	target := storetest.New(t)
	first, second := target.Open(t), target.Open(t)
	period := time.Now().UTC().Format("2006-01-02")
	for i, client := range []*store.Store{first, second} {
		team := tenant(t, client, store.DefaultTeam)
		for _, counter := range []string{"egress_bytes", "download_bytes", "storage_bytes", "storage_objects", "runs"} {
			if err := team.AddUsage(t.Context(), store.UsageDelta{Period: "fixture-period", ReplicaID: fmt.Sprint(i), Counter: counter, Amount: []int64{7, 11}[i]}); err != nil {
				t.Fatal(err)
			}
		}
		if err := team.AddUsage(t.Context(), store.UsageDelta{Period: period, ReplicaID: fmt.Sprint(i), Counter: "egress_bytes", Amount: []int64{7, 11}[i]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	second = target.Open(t)
	for _, query := range []string{
		"SELECT SUM(bytes) FROM egress_usage WHERE team=? AND period='fixture-period'",
		"SELECT SUM(bytes) FROM team_download_day WHERE team=? AND period='fixture-period'",
		"SELECT SUM(bytes) FROM storage_month_usage WHERE team=? AND period='fixture-period'",
		"SELECT SUM(objects) FROM storage_month_usage WHERE team=? AND period='fixture-period'",
		"SELECT SUM(runs) FROM run_rate_usage WHERE team=? AND period='fixture-period' AND principal=''",
	} {
		var total int64
		if err := second.DB().QueryRowContext(t.Context(), storetest.Rebind(second, query), store.DefaultTeam).Scan(&total); err != nil {
			t.Fatal(err)
		}
		if total != 18 {
			t.Fatalf("durable replica sum=%d, want 18", total)
		}
	}
	for _, client := range []*store.Store{first, second} {
		team := tenant(t, client, store.DefaultTeam)
		for _, selected := range []string{period, period[:7]} {
			usage, err := team.EgressUsage(t.Context(), selected)
			if err != nil || usage.Team != store.DefaultTeam || usage.Bytes != 18 {
				t.Fatalf("egress replica report=%+v: %v", usage, err)
			}
		}
		usage, err := team.StorageUsageFor(t.Context(), "", "fixture-period")
		if err != nil || usage.Team != store.DefaultTeam || usage.MonthBytes != 18 || usage.MonthObjects != 18 {
			t.Fatalf("storage replica report=%+v: %v", usage, err)
		}
	}
}

func TestReplicaQuotaReservationSerializesAndReleases(t *testing.T) {
	target := storetest.New(t)
	first, second := target.Open(t), target.Open(t)
	teams := []*store.Tenant{tenant(t, first, store.DefaultTeam), tenant(t, second, store.DefaultTeam)}
	if err := teams[0].SetStorageQuota(t.Context(), store.StorageQuota{MaxBytesPerMonth: 100}); err != nil {
		t.Fatal(err)
	}
	quota, err := teams[1].SetStorageAllowance(t.Context(), 50)
	if err != nil || quota.MaxBytesPerMonth != 100 || quota.AllowanceBytes != 50 {
		t.Fatalf("shared quota configuration=%+v: %v", quota, err)
	}
	prior, err := teams[0].ReserveQuota(t.Context(), "storage", 90)
	if err != nil {
		t.Fatal(err)
	}
	if err := teams[0].SettleQuota(t.Context(), prior.ID, 90); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type result struct {
		reservation store.QuotaReservation
		err         error
	}
	results := make(chan result, len(teams))
	for _, team := range teams {
		go func() {
			<-start
			r, err := team.ReserveQuota(t.Context(), "storage", 8)
			results <- result{r, err}
		}()
	}
	close(start)
	var reservation store.QuotaReservation
	winners := 0
	var raceErrors error
	for range teams {
		r := <-results
		if r.err == nil {
			winners++
			reservation = r.reservation
		} else if !errors.Is(r.err, store.ErrStorageQuota) {
			raceErrors = errors.Join(raceErrors, r.err)
		}
	}
	if raceErrors != nil || winners != 1 {
		t.Fatalf("quota race has %d winners, want one: %v", winners, raceErrors)
	}
	for range 2 {
		if err := teams[0].SettleQuota(t.Context(), reservation.ID, 0); err != nil {
			t.Fatal(err)
		}
	}
	var used, reserved int64
	if err := second.DB().QueryRowContext(t.Context(), storetest.Rebind(second, "SELECT used,reserved FROM quota_admission WHERE team=? AND quota='storage'"), store.DefaultTeam).Scan(&used, &reserved); err != nil {
		t.Fatal(err)
	}
	if used != 90 || reserved != 0 {
		t.Fatalf("released quota=(%d,%d), want (90,0)", used, reserved)
	}
	if _, err := teams[1].ReserveQuota(t.Context(), "storage", 8); err != nil {
		t.Fatalf("release did not restore capacity: %v", err)
	}
}

func TestReplicaMaintenanceLeaseHasOneWinner(t *testing.T) {
	target := storetest.New(t)
	clients := []*store.Store{target.Open(t), target.Open(t)}
	start, results := make(chan struct{}), make(chan error, len(clients))
	for i, client := range clients {
		go func() {
			<-start
			_, err := client.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", fmt.Sprint(i), time.Hour)
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
	if raceErrors != nil || winners != 1 {
		t.Fatalf("singleton race has %d winners, want one: %v", winners, raceErrors)
	}
}

func TestReplicaDownloadCapSurvivesReopening(t *testing.T) {
	target := storetest.New(t)
	first, second := target.Open(t), target.Open(t)
	team := store.Team("download-team")
	if err := first.AsOperator().TestOnlyCreateTeam(t.Context(), team); err != nil {
		t.Fatal(err)
	}
	teams := []*store.Tenant{tenant(t, first, team), tenant(t, second, team)}
	if _, err := teams[0].ChargeDownload(t.Context(), store.DownloadCharge{ReplicaID: "first", Bytes: 90, FreeCapBytes: 100}); err != nil {
		t.Fatal(err)
	}
	start, results := make(chan struct{}), make(chan error, len(teams))
	for i, selected := range teams {
		go func() {
			<-start
			_, err := selected.ChargeDownload(t.Context(), store.DownloadCharge{ReplicaID: fmt.Sprint(i), Bytes: 10, FreeCapBytes: 100})
			results <- err
		}()
	}
	close(start)
	winners := 0
	var raceErrors error
	for range teams {
		if err := <-results; err == nil {
			winners++
		} else if !errors.Is(err, store.ErrDownloadCap) {
			raceErrors = errors.Join(raceErrors, err)
		}
	}
	if raceErrors != nil || winners != 1 {
		t.Fatalf("download race has %d winners, want one: %v", winners, raceErrors)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	second = target.Open(t)
	if _, err := tenant(t, second, team).ChargeDownload(t.Context(), store.DownloadCharge{ReplicaID: "first", FreeCapBytes: 100}); !errors.Is(err, store.ErrDownloadCap) {
		t.Fatalf("restart lost download cap: %v", err)
	}
	var total int64
	if err := second.DB().QueryRowContext(t.Context(), storetest.Rebind(second, "SELECT SUM(bytes) FROM team_download_day WHERE team=?"), team).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 100 {
		t.Fatalf("replica downloads=%d, want 100", total)
	}
}
