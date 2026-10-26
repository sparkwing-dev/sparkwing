package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

const gib = int64(1) << 30

// chargeableTeam prices every stored byte and gives a signed-up team a run of its own and an explicit quota.
func chargeableTeam(t *testing.T, st *store.Store, team store.Team, quota bool) *store.Tenant {
	t.Helper()
	ctx := context.Background()
	rate, free := int64(store.CloudStorageRateMicroPerGBDay), int64(0)
	if _, err := st.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{
		StorageRateMicroPerGBDay: &rate, StorageFreeAllowanceBytes: &free,
	}); err != nil {
		t.Fatalf("set storage settings: %v", err)
	}
	handle := teamHandle(t, st, team)
	if quota {
		if err := handle.SetStorageQuota(ctx, store.StorageQuota{Team: team, MaxBytesPerRun: 1 << 40}); err != nil {
			t.Fatalf("set quota: %v", err)
		}
	}
	teamRun(t, handle, "r1")
	return handle
}

// A team with no quota row is refused at zero balance too, because the refusal
// follows the price and not the quota.
func TestAnEmptyBalanceRefusesATeamWithNoQuotaRow(t *testing.T) {
	st := storetest.Open(t)
	acme := chargeableTeam(t, st, "acme", false)
	err := acme.AppendEvent(context.Background(), "r1", "n1", "log", []byte("bytes"))
	if !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("append = %v, want an insufficient-credits refusal", err)
	}
}

func TestAnEmptyBalanceRefusesAWriteThatGrowsRetainedBytes(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme := chargeableTeam(t, st, "acme", true)

	err := acme.AppendEvent(ctx, "r1", "n1", "log", []byte("more bytes"))
	if !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("append on an empty balance = %v, want an insufficient-credits refusal", err)
	}
	if !strings.Contains(err.Error(), "acme") {
		t.Fatalf("refusal = %v, want one naming the team", err)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM events WHERE team='acme'`); got != 0 {
		t.Fatalf("events = %d, want the refused write stored nothing", got)
	}

	if _, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 10 * store.MicroCreditsPerCent, Reference: "storage-funding", CreatedBy: "operator"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := acme.AppendEvent(ctx, "r1", "n1", "log", []byte("more bytes")); err != nil {
		t.Fatalf("append once the balance holds credit: %v", err)
	}
	retained, err := acme.StorageRetainedBytes(ctx)
	if err != nil {
		t.Fatalf("retained: %v", err)
	}
	if retained != 0 {
		t.Fatalf("retained = %d, want no blob or log bytes", retained)
	}
}

// Negative control: the operator's own team has no free allowance and is never refused for its balance.
func TestTheOperatorsTeamIsNotRefusedForItsBalance(t *testing.T) {
	st := storetest.Open(t)
	chargeableTeam(t, st, "acme", false)
	seedRunWithNode(t, st, "op", "n1", "running")
	if err := storageTestTenant(t, st).AppendEvent(context.Background(), "op", "n1", "log", []byte("bytes")); err != nil {
		t.Fatalf("operator append on an empty balance: %v", err)
	}
}

func TestStorageSettingsRefuseValuesOutsideTheirBounds(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	for _, up := range []store.CreditSettingsUpdate{
		{StorageRateMicroPerGBDay: ptrInt64(-1)},
		{StorageRateMicroPerGBDay: ptrInt64(1000000000000 + 1)},
		{StorageFreeAllowanceBytes: ptrInt64(-1)},
	} {
		if _, err := st.AsOperator().SetCreditSettings(ctx, up); !errors.Is(err, store.ErrInvalidCreditSetting) {
			t.Fatalf("SetCreditSettings(%+v) = %v, want an invalid-setting refusal", up, err)
		}
	}
	settings, err := st.AsOperator().CreditSettings(ctx)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if settings.StorageRateMicroPerGBDay != 0 || settings.StorageFreeAllowanceBytes != gib {
		t.Fatalf("settings = %+v, want the refusals to have moved nothing", settings)
	}
}

func ptrInt64(v int64) *int64 { return &v }
