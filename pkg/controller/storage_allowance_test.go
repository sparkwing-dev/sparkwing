package controller_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const storageAllowancePath = "/api/v1/storage/quotas/acme/allowance"

func licenseStorageMetering(t *testing.T, f storageFixture) {
	t.Helper()
	raw, pub := multiTeamLicense(t)
	f.server.WithLicense(license.Resolve(raw, pub, time.Now(), nil))
}

func TestUnlicensedStorageWriteIgnoresCreditRate(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Principal: "acme", MaxBytesPerRun: 1 << 20})
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
	f := newStorageFixture(t, store.StorageQuota{Principal: "acme", MaxBytesPerRun: 1 << 40})
	ctx := context.Background()
	rate, free := int64(store.CloudStorageRateMicroPerGBDay), int64(0)
	if _, err := f.store.SetCreditSettings(ctx, store.CreditSettingsUpdate{
		StorageRateMicroPerGBDay: &rate, StorageFreeAllowanceBytes: &free,
	}); err != nil {
		t.Fatal(err)
	}
	seedRetainedRuns(t, f.store, "acme", 1, 1<<30)
	if _, err := f.store.ChargeRetainedStorage(ctx, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.server.MaintainStorage(ctx)
	charges, err := f.store.ListCreditCharges(ctx, 10)
	if err != nil || len(charges) != 0 {
		t.Fatalf("unlicensed storage charges = %+v, %v", charges, err)
	}
}

func TestStorageAllowanceRouteReadsAndSetsOnTheAdminToken(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Principal: "acme", MaxBytesPerRun: 1 << 20})

	code, body := f.request(t, http.MethodPut, storageAllowancePath, f.admin,
		`{"storage_allowance_bytes":53687091200}`, false)
	if code != http.StatusOK {
		t.Fatalf("setting the allowance = %d %s, want 200", code, body)
	}
	var quota struct {
		Principal             string `json:"principal"`
		MaxBytesPerRun        int64  `json:"max_bytes_per_run"`
		StorageAllowanceBytes int64  `json:"storage_allowance_bytes"`
	}
	if err := json.Unmarshal([]byte(body), &quota); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if quota.Principal != "acme" || quota.StorageAllowanceBytes != 53_687_091_200 {
		t.Fatalf("quota = %+v, want acme keeping fifty gibibytes", quota)
	}
	if quota.MaxBytesPerRun != 1<<20 {
		t.Fatalf("quota = %+v, want the per-run limit left alone", quota)
	}

	code, body = f.request(t, http.MethodGet, "/api/v1/storage", f.admin, "", false)
	if code != http.StatusOK {
		t.Fatalf("storage show = %d %s, want 200", code, body)
	}
	if !strings.Contains(body, `"storage_allowance_bytes":53687091200`) {
		t.Fatalf("storage show %q does not report the allowance", body)
	}
	if !strings.Contains(body, `"retained_bytes"`) {
		t.Fatalf("storage show %q does not report the retained bytes", body)
	}

	// safety: the quota route is not a second writer of the allowance, so a
	// quota rewrite keeps what the team asked to keep.
	if code, body := f.request(t, http.MethodPut, "/api/v1/storage/quotas/acme", f.admin,
		`{"tier":"paid"}`, false); code != http.StatusOK {
		t.Fatalf("quota rewrite = %d %s, want 200", code, body)
	}
	stored, err := f.store.StorageQuotaFor(context.Background(), "acme")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if stored.AllowanceBytes != 53_687_091_200 {
		t.Fatalf("quota = %+v, want the allowance kept through a quota rewrite", stored)
	}
}

// The pass sweeps before it bills, so a team is never billed for the bytes the
// sweep is about to expire; the allowance is the most it pays for.
func TestTheStoragePassSweepsBeforeItBills(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newStorageFixture(t, store.StorageQuota{Principal: "acme", MaxBytesPerRun: 1 << 40})
	licenseStorageMetering(t, f)
	ctx := context.Background()
	rate, free := int64(store.CloudStorageRateMicroPerGBDay), int64(0)
	if _, err := f.store.SetCreditSettings(ctx, store.CreditSettingsUpdate{
		StorageRateMicroPerGBDay: &rate, StorageFreeAllowanceBytes: &free,
	}); err != nil {
		t.Fatalf("set the storage rate: %v", err)
	}
	if _, err := f.store.SetStorageAllowance(ctx, "acme", 1<<30); err != nil {
		t.Fatalf("set allowance: %v", err)
	}
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantPaid,
		1_000*store.MicroCreditsPerCent, "pay_1", "operator"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	seedRetainedRuns(t, f.store, "acme", 100, 1<<30)

	f.server.MaintainStorage(ctx)
	retained, err := f.store.StorageRetainedBytes(ctx, "acme")
	if err != nil {
		t.Fatalf("retained: %v", err)
	}
	if retained != 1<<30 {
		t.Fatalf("retained = %d, want the sweep to have applied the allowance", retained)
	}
	billed, err := f.store.ChargeRetainedStorage(ctx, time.Now().UTC().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	if len(billed.Charges) != 1 || billed.Charges[0].StorageBytes != 1<<30 {
		t.Fatalf("charges = %+v, want the one gibibyte the team keeps", billed.Charges)
	}
}

// safety: this fixture's store is the SQLite one the controller tests open, so
// the placeholder needs no dialect rewrite.
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

func TestStorageAllowanceRouteRefusesANonAdminTokenAndABadValue(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Principal: "acme", MaxBytesPerRun: 1 << 20})

	if code, body := f.request(t, http.MethodPut, storageAllowancePath, f.team,
		`{"storage_allowance_bytes":1}`, false); code != http.StatusForbidden {
		t.Fatalf("a runner token setting an allowance = %d %s, want 403", code, body)
	}
	if code, body := f.request(t, http.MethodPut, storageAllowancePath, f.admin,
		`{"storage_allowance_bytes":-1}`, false); code != http.StatusBadRequest {
		t.Fatalf("a negative allowance = %d %s, want 400", code, body)
	}
	quota, err := f.store.StorageQuotaFor(context.Background(), "acme")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if quota.AllowanceBytes != 0 {
		t.Fatalf("quota = %+v, want the refusals to have moved nothing", quota)
	}
}

func TestAnEmptyBalanceRefusesAChargedWriteWithPaymentRequired(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Principal: "acme", MaxBytesPerRun: 1 << 20})
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

func TestStorageMaintenanceBillsRetainedBytesAndReportsThemOnCreditsShow(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newStorageFixture(t, store.StorageQuota{Principal: "acme", MaxBytesPerRun: 1 << 20})
	licenseStorageMetering(t, f)
	ctx := context.Background()
	// safety: a handful of event bytes at the cloud rate truncates to nothing,
	// so this rate is what makes a few bytes worth a micro-credit at all.
	rate, free := int64(1_000_000_000), int64(0)
	if _, err := f.store.SetCreditSettings(ctx, store.CreditSettingsUpdate{
		StorageRateMicroPerGBDay: &rate, StorageFreeAllowanceBytes: &free,
	}); err != nil {
		t.Fatalf("set the storage rate: %v", err)
	}
	if _, err := f.store.GrantCredits(ctx, store.CreditGrantPaid,
		100*store.MicroCreditsPerCent, "pay_1", "operator"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if code, body := f.request(t, http.MethodPost, storageEventPath, f.team,
		eventBody("bytes"), true); code != http.StatusOK {
		t.Fatalf("seed a charged write = %d %s, want 200", code, body)
	}

	// safety: the first pass only stamps the team, which is what keeps the
	// sample below at zero.
	f.server.MaintainStorage(ctx)
	state := f.creditState(t)
	if state.StorageRateMicroPerGBDay != rate || state.StorageFreeAllowanceBytes != 0 {
		t.Fatalf("credits show = %+v, want the storage settings it was given", state)
	}
	if state.StorageChargedMicro != 0 {
		t.Fatalf("credits show = %+v, want nothing billed inside the first interval", state)
	}

	billed, err := f.store.ChargeRetainedStorage(ctx, time.Now().UTC().Add(48*time.Hour))
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	if len(billed.Charges) != 1 || billed.Charges[0].Principal != "acme" {
		t.Fatalf("charges = %+v, want one for acme", billed.Charges)
	}
	if state = f.creditState(t); state.StorageChargedMicro != billed.ChargedMicro {
		t.Fatalf("credits show = %+v, want the %d micro the pass billed", state, billed.ChargedMicro)
	}

	code, body := f.request(t, http.MethodGet, "/api/v1/credits/history", f.admin, "", false)
	if code != http.StatusOK {
		t.Fatalf("credits history = %d %s, want 200", code, body)
	}
	if !strings.Contains(body, `"kind":"`+store.CreditChargeStorage+`"`) ||
		!strings.Contains(body, `"principal":"acme"`) {
		t.Fatalf("credits history %q does not separate the storage charge", body)
	}
}

type creditStateView struct {
	StorageChargedMicro       int64 `json:"storage_charged_micro"`
	StorageRateMicroPerGBDay  int64 `json:"storage_rate_micro_per_gb_day"`
	StorageFreeAllowanceBytes int64 `json:"storage_free_allowance_bytes"`
}

func (f storageFixture) creditState(t *testing.T) creditStateView {
	t.Helper()
	code, body := f.request(t, http.MethodGet, "/api/v1/credits", f.admin, "", false)
	if code != http.StatusOK {
		t.Fatalf("credits show = %d %s, want 200", code, body)
	}
	var out creditStateView
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return out
}
