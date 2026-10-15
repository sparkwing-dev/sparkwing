package controller_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const storageAllowancePath = "/api/v1/operator/object-store/quotas/acme/allowance"

func licenseStorageMetering(t *testing.T, f storageFixture) {
	t.Helper()
	raw, pub := multiTeamLicense(t)
	f.server.WithLicense(license.Resolve(raw, pub, time.Now(), nil))
}

func TestUnlicensedStorageWriteIgnoresCreditRate(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Team: store.DefaultTeam, MaxBytesPerRun: 1 << 20})
	rate := int64(store.CloudStorageRateMicroPerGBDay)
	if _, err := f.store.SetCreditSettings(context.Background(), store.CreditSettingsUpdate{
		StorageRateMicroPerGBDay: &rate,
	}); err != nil {
		t.Fatal(err)
	}
	if code, body := f.request(t, http.MethodPost, storageEventPath, f.team, eventBody("bytes"), true); code != http.StatusOK {
		t.Fatalf("unlicensed write with no balance = %d %s, want 200", code, body)
	}
}

func TestUnlicensedStorageMaintenanceWritesNoCharges(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Team: store.DefaultTeam, MaxBytesPerRun: 1 << 40})
	ctx := context.Background()
	rate, free := int64(store.CloudStorageRateMicroPerGBDay), int64(0)
	if _, err := f.store.SetCreditSettings(ctx, store.CreditSettingsUpdate{
		StorageRateMicroPerGBDay: &rate, StorageFreeAllowanceBytes: &free,
	}); err != nil {
		t.Fatal(err)
	}
	seedRetainedRuns(t, f.store, "acme", 1, 1<<30)
	f.server.MaintainStorage(ctx)
	charges, err := f.store.ListCreditCharges(ctx, 10)
	if err != nil || len(charges) != 0 {
		t.Fatalf("unlicensed storage charges = %+v, %v", charges, err)
	}
}

func seedRetainedRuns(t *testing.T, st *store.Store, principal string, runs int, bytes int64) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for i := range runs {
		id := fmt.Sprintf("seed-%d", i)
		if err := st.CreateRun(ctx, store.Run{
			ID: id, Pipeline: "p", Status: "success", StartedAt: now,
		}); err != nil {
			t.Fatalf("create run %s: %v", id, err)
		}
		if _, err := st.DB().Exec(`UPDATE runs SET created_at = ? WHERE id = ?`,
			now.Add(-time.Duration(runs-i)*time.Hour).UnixNano(), id); err != nil {
			t.Fatalf("backdate run %s: %v", id, err)
		}
		if _, err := st.DB().Exec(
			`INSERT INTO storage_run_usage (principal, run_id, bytes, objects, updated_at)
			 VALUES (?, ?, ?, 0, ?)`, principal, id, bytes, now.UnixNano()); err != nil {
			t.Fatalf("seed bytes for %s: %v", id, err)
		}
	}
}

func TestAnEmptyBalanceRefusesAChargedWriteWithPaymentRequired(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Team: store.DefaultTeam, MaxBytesPerRun: 1 << 20})
	licenseStorageMetering(t, f)
	ctx := context.Background()
	rate := int64(store.CloudStorageRateMicroPerGBDay)
	if _, err := f.store.SetCreditSettings(ctx, store.CreditSettingsUpdate{
		StorageRateMicroPerGBDay: &rate,
	}); err != nil {
		t.Fatalf("set the storage rate: %v", err)
	}

	code, body := f.request(t, http.MethodPost, storageEventPath, f.team, eventBody("bytes"), true)
	if code != http.StatusPaymentRequired {
		t.Fatalf("an event on an empty balance = %d %s, want 402", code, body)
	}
	if !strings.Contains(body, "acme") || !strings.Contains(body, "insufficient credits") {
		t.Fatalf("the refusal reads %q and does not say why", body)
	}

	if _, err := f.store.GrantCredits(ctx, store.CreditGrantPaid,
		100*store.MicroCreditsPerCent, "pay_1", "operator"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if code, body := f.request(t, http.MethodPost, storageEventPath, f.team,
		eventBody("bytes"), true); code != http.StatusOK {
		t.Fatalf("an event once the balance holds credit = %d %s, want 200", code, body)
	}
}
