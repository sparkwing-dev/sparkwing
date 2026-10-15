package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

const gib = int64(1) << 30

func seedRetainedRun(t *testing.T, st *store.Store, principal, runID string, bytes int64, created time.Time) {
	t.Helper()
	seedRunWithNode(t, st, runID, "n", "success")
	if _, err := st.DB().Exec(storetest.Rebind(st,
		`UPDATE runs SET created_at = ?, finished_at = ? WHERE id = ?`), created.UnixNano(), created.UnixNano(), runID); err != nil {
		t.Fatalf("backdate run %s: %v", runID, err)
	}
	if _, err := st.DB().Exec(storetest.Rebind(st,
		`INSERT INTO storage_run_usage (principal, run_id, bytes, objects, updated_at)
		 VALUES (?, ?, ?, 0, ?)`),
		principal, runID, bytes, created.UnixNano()); err != nil {
		t.Fatalf("seed retained bytes for %s: %v", runID, err)
	}
}

func chargeableTeam(t *testing.T, st *store.Store, principal string, rate, free int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.SetCreditSettings(ctx, store.CreditSettingsUpdate{
		StorageRateMicroPerGBDay: &rate, StorageFreeAllowanceBytes: &free,
	}); err != nil {
		t.Fatalf("set storage settings: %v", err)
	}
	if err := st.SetStorageQuota(ctx, store.StorageQuota{
		Principal: principal, MaxBytesPerRun: 1 << 40,
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
	_, err := st.AppendEventCharged(ctx, "nomad", "r1", "n1", "log", []byte("bytes"))
	if !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("append = %v, want an insufficient-credits refusal", err)
	}
}

func TestAnEmptyBalanceRefusesAWriteThatGrowsRetainedBytes(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, gib)
	seedRunWithNode(t, st, "r1", "n1", "running")

	_, err := st.AppendEventCharged(ctx, "acme", "r1", "n1", "log", []byte("more bytes"))
	if !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("append on an empty balance = %v, want an insufficient-credits refusal", err)
	}
	if !strings.Contains(err.Error(), "acme") {
		t.Fatalf("refusal = %v, want one naming the team", err)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM events`); got != 0 {
		t.Fatalf("events = %d, want the refused write stored nothing", got)
	}

	if _, err := st.GrantCredits(ctx, store.CreditGrantFree, 10*store.MicroCreditsPerCent,
		"", "operator"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := st.AppendEventCharged(ctx, "acme", "r1", "n1", "log", []byte("more bytes")); err != nil {
		t.Fatalf("append once the balance holds credit: %v", err)
	}
	retained, err := st.StorageRetainedBytes(ctx, "acme")
	if err != nil {
		t.Fatalf("retained: %v", err)
	}
	if retained != int64(len("log")+len("more bytes")) {
		t.Fatalf("retained = %d, want the kind and payload just written", retained)
	}
}

func TestTheSweepExpiresTheOldestRunsAboveTheAllowance(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, gib)
	now := time.Unix(1_700_000_000, 0).UTC()
	for i, runID := range []string{"oldest", "middle", "newest"} {
		seedRetainedRun(t, st, "acme", runID, gib, now.Add(-time.Duration(3-i)*time.Hour))
	}
	if _, err := st.SetStorageAllowance(ctx, "acme", 2*gib); err != nil {
		t.Fatalf("set allowance: %v", err)
	}

	swept, err := st.SweepStorageAllowance(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept.Runs != 1 || swept.Bytes != gib {
		t.Fatalf("swept %+v, want the oldest run alone", swept)
	}
	retained, err := st.StorageRetainedBytes(ctx, "acme")
	if err != nil {
		t.Fatalf("retained: %v", err)
	}
	if retained != 2*gib {
		t.Fatalf("retained = %d, want the allowance", retained)
	}
	if got := countRows(t, st,
		`SELECT COUNT(*) FROM storage_run_usage WHERE run_id = 'oldest'`); got != 0 {
		t.Fatalf("the oldest run still holds %d usage rows", got)
	}
}

func TestASpentBalanceDrainsToTheFreeAllowanceOutsideTheRetentionWindow(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, gib)
	if err := st.SetStorageSettings(ctx, store.StorageSettings{EventRetentionDays: 30}); err != nil {
		t.Fatalf("set retention: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "expired", gib, now.Add(-40*24*time.Hour))
	seedRetainedRun(t, st, "acme", "older", gib, now.Add(-35*24*time.Hour))
	seedRetainedRun(t, st, "acme", "inside", gib, now.Add(-2*24*time.Hour))

	swept, err := st.SweepStorageAllowance(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	// safety: the team named no allowance, so only the unpaid drain runs and
	// it stops at the run the retention window still covers.
	if swept.Runs != 2 || swept.Bytes != 2*gib {
		t.Fatalf("swept %+v, want the two runs past the window", swept)
	}
	retained, err := st.StorageRetainedBytes(ctx, "acme")
	if err != nil {
		t.Fatalf("retained: %v", err)
	}
	if retained != gib {
		t.Fatalf("retained = %d, want the free allowance", retained)
	}
	if got := countRows(t, st,
		`SELECT COUNT(*) FROM storage_run_usage WHERE run_id = 'inside'`); got != 1 {
		t.Fatalf("the run inside the window lost its bytes for non-payment")
	}
}

// The drain's window is measured from when a run finished. A run created
// before the window that finished inside it keeps its bytes; a drain keyed
// on creation would take a run that just ended.
func TestASpentBalanceDrainsByWhenARunFinished(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, 0)
	if err := st.SetStorageSettings(ctx, store.StorageSettings{EventRetentionDays: 30}); err != nil {
		t.Fatalf("set retention: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "expired", gib, now.Add(-40*24*time.Hour))
	seedRetainedRun(t, st, "acme", "long", gib, now.Add(-40*24*time.Hour))
	if _, err := st.DB().Exec(storetest.Rebind(st, `UPDATE runs SET finished_at = ? WHERE id = 'long'`),
		now.Add(-24*time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	swept, err := st.SweepStorageAllowance(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept.Runs != 1 || swept.Bytes != gib {
		t.Fatalf("swept %+v, want only the run that finished past the window", swept)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM storage_run_usage WHERE run_id = 'long'`); got != 1 {
		t.Fatal("a run that finished inside the window lost its bytes for non-payment")
	}
}

func TestASpentBalanceWithNoFreeAllowanceDrainsEverythingPastTheWindow(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, 0)
	if err := st.SetStorageSettings(ctx, store.StorageSettings{EventRetentionDays: 30}); err != nil {
		t.Fatalf("set retention: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "expired", gib, now.Add(-40*24*time.Hour))
	seedRetainedRun(t, st, "acme", "inside", gib, now.Add(-2*24*time.Hour))

	swept, err := st.SweepStorageAllowance(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept.Runs != 1 || swept.Bytes != gib {
		t.Fatalf("swept %+v, want the run past the window alone", swept)
	}
	retained, err := st.StorageRetainedBytes(ctx, "acme")
	if err != nil {
		t.Fatalf("retained: %v", err)
	}
	if retained != gib {
		t.Fatalf("retained = %d, want only what the window still covers", retained)
	}
}

func TestSweepKeepsEverythingForATeamThatNamedNoAllowance(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := st.SetStorageQuota(ctx, store.StorageQuota{
		Principal: "acme", MaxBytesPerRun: 1 << 40,
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	seedRetainedRun(t, st, "acme", "ancient", 3*gib, now.Add(-365*24*time.Hour))

	swept, err := st.SweepStorageAllowance(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept.Runs != 0 {
		t.Fatalf("swept %+v, want nothing on an install that priced no storage", swept)
	}
}

func TestAPaidBalanceDrainsNothingBelowTheAllowance(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, gib)
	if err := st.SetStorageSettings(ctx, store.StorageSettings{EventRetentionDays: 30}); err != nil {
		t.Fatalf("set retention: %v", err)
	}
	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCent,
		"pay_1", "operator"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "ancient", 3*gib, now.Add(-90*24*time.Hour))

	swept, err := st.SweepStorageAllowance(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept.Runs != 0 {
		t.Fatalf("swept %+v, want nothing while the balance pays", swept)
	}
}

func TestSetStorageAllowanceLeavesTheOtherLimitsAlone(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := st.SetStorageQuota(ctx, store.StorageQuota{
		Principal: "acme", Tier: store.StorageTierPaid,
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	quota, err := st.SetStorageAllowance(ctx, "acme", 50*gib)
	if err != nil {
		t.Fatalf("set allowance: %v", err)
	}
	if quota.AllowanceBytes != 50*gib || quota.Tier != store.StorageTierPaid ||
		quota.MaxBytesPerRun != store.PaidTierQuota.MaxBytesPerRun {
		t.Fatalf("quota = %+v, want the paid tier with a 50 GiB allowance", quota)
	}
	if _, err := st.SetStorageAllowance(ctx, "acme", -1); err == nil {
		t.Fatal("a negative allowance was accepted")
	}
}

// The allowance has one writer, so a quota rewrite that says nothing about it
// leaves it alone rather than zeroing what a team asked to keep.
func TestAQuotaRewriteKeepsTheAllowance(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if _, err := st.SetStorageAllowance(ctx, "acme", 50*gib); err != nil {
		t.Fatalf("set allowance: %v", err)
	}
	if err := st.SetStorageQuota(ctx, store.StorageQuota{
		Principal: "acme", Tier: store.StorageTierPaid,
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	quota, err := st.StorageQuotaFor(ctx, "acme")
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if quota.AllowanceBytes != 50*gib || quota.MaxBytesPerRun != store.PaidTierQuota.MaxBytesPerRun {
		t.Fatalf("quota = %+v, want the paid tier with the allowance kept", quota)
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
