package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const storageAllowancePath = "/api/v1/storage/quotas/acme/allowance"

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
		100*store.MicroCreditsPerCredit, "pay_1", "operator"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if code, body := f.request(t, http.MethodPost, storageEventPath, f.team,
		eventBody("bytes"), true); code != http.StatusOK {
		t.Fatalf("an event once the balance holds credit = %d %s, want 200", code, body)
	}
}

func TestStorageMaintenanceBillsRetainedBytesAndReportsThemOnCreditsShow(t *testing.T) {
	f := newStorageFixture(t, store.StorageQuota{Principal: "acme", MaxBytesPerRun: 1 << 20})
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
		100*store.MicroCreditsPerCredit, "pay_1", "operator"); err != nil {
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
