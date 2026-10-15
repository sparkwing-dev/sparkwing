package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

const gib = int64(1) << 30

func chargeableTeam(t *testing.T, st *store.Store, principal string, rate, free int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.SetCreditSettings(ctx, store.CreditSettingsUpdate{
		StorageRateMicroPerGBDay: &rate, StorageFreeAllowanceBytes: &free,
	}); err != nil {
		t.Fatalf("set storage settings: %v", err)
	}
	if err := storageTestTenant(t, st).SetStorageQuota(ctx, store.StorageQuota{
		Team: store.DefaultTeam, MaxBytesPerRun: 1 << 40,
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
}

// A team with no quota row is refused at zero balance too, because the refusal
// follows the price and not the quota.
func TestAnEmptyBalanceRefusesATeamWithNoQuotaRow(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	rate := int64(store.CloudStorageRateMicroPerGBDay)
	if _, err := st.SetCreditSettings(ctx, store.CreditSettingsUpdate{
		StorageRateMicroPerGBDay: &rate,
	}); err != nil {
		t.Fatalf("set storage settings: %v", err)
	}
	seedRunWithNode(t, st, "r1", "n1", "running")
	_, err := storageTestTenant(t, st).AppendEventCharged(ctx, "replica", "r1", "n1", "log", []byte("bytes"))
	if !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("append = %v, want an insufficient-credits refusal", err)
	}
}

func TestAnEmptyBalanceRefusesAWriteThatGrowsRetainedBytes(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, gib)
	seedRunWithNode(t, st, "r1", "n1", "running")

	_, err := storageTestTenant(t, st).AppendEventCharged(ctx, "replica", "r1", "n1", "log", []byte("more bytes"))
	if !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("append on an empty balance = %v, want an insufficient-credits refusal", err)
	}
	if !strings.Contains(err.Error(), string(store.DefaultTeam)) {
		t.Fatalf("refusal = %v, want one naming the team", err)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM events`); got != 0 {
		t.Fatalf("events = %d, want the refused write stored nothing", got)
	}

	if _, err := st.GrantCredits(ctx, store.CreditGrantFree, 10*store.MicroCreditsPerCent,
		"", "operator"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := storageTestTenant(t, st).AppendEventCharged(ctx, "replica", "r1", "n1", "log", []byte("more bytes")); err != nil {
		t.Fatalf("append once the balance holds credit: %v", err)
	}
	retained, err := storageTestTenant(t, st).StorageRetainedBytes(ctx)
	if err != nil {
		t.Fatalf("retained: %v", err)
	}
	if retained != 0 {
		t.Fatalf("retained = %d, want no blob or log bytes", retained)
	}
}

func TestStorageSettingsRefuseValuesOutsideTheirBounds(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	for _, up := range []store.CreditSettingsUpdate{
		{StorageRateMicroPerGBDay: ptrInt64(-1)},
		{StorageRateMicroPerGBDay: ptrInt64(store.MaxStorageRateMicroPerGBDay + 1)},
		{StorageFreeAllowanceBytes: ptrInt64(-1)},
	} {
		if _, err := st.SetCreditSettings(ctx, up); !errors.Is(err, store.ErrInvalidCreditSetting) {
			t.Fatalf("SetCreditSettings(%+v) = %v, want an invalid-setting refusal", up, err)
		}
	}
	settings, err := st.CreditSettings(ctx)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if settings.StorageRateMicroPerGBDay != 0 || settings.StorageFreeAllowanceBytes != 0 {
		t.Fatalf("settings = %+v, want the refusals to have moved nothing", settings)
	}
}

func ptrInt64(v int64) *int64 { return &v }
