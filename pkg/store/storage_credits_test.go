package store_test

import (
	"context"
	"errors"
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
		`UPDATE runs SET created_at = ? WHERE id = ?`), created.UnixNano(), runID); err != nil {
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

// safety: the first pass only stamps the team, so an interval is billed by the
// two passes a controller makes over two timer ticks rather than by one.
func billOneInterval(t *testing.T, st *store.Store, from time.Time, d time.Duration) store.StorageChargeSweep {
	t.Helper()
	ctx := context.Background()
	first, err := st.ChargeRetainedStorage(ctx, from)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if len(first.Charges) != 0 {
		t.Fatalf("the first pass billed %+v, want a stamp and no charge", first.Charges)
	}
	billed, err := st.ChargeRetainedStorage(ctx, from.Add(d))
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	return billed
}

func TestStorageChargeBillsTheBytesAboveTheFreeAllowanceForADay(t *testing.T) {
	st := storetest.Open(t)
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, gib)
	start := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "r1", 3*gib, start.Add(-time.Hour))

	billed := billOneInterval(t, st, start, 24*time.Hour)
	if len(billed.Charges) != 1 {
		t.Fatalf("charges = %+v, want one", billed.Charges)
	}
	charge := billed.Charges[0]
	if charge.AmountMicro != 2*store.CloudStorageRateMicroPerGBDay {
		t.Fatalf("amount = %d, want %d", charge.AmountMicro, 2*store.CloudStorageRateMicroPerGBDay)
	}
	if charge.Kind != store.CreditChargeStorage || charge.Principal != "acme" {
		t.Fatalf("charge = %+v, want a storage row for acme", charge)
	}
	if charge.StorageBytes != 2*gib || charge.Seconds != 86_400 {
		t.Fatalf("charge = %+v, want 2 GiB over 86400s", charge)
	}
	if charge.CPUClassCores != 0 || charge.RateMicroPerSecond != 0 || charge.RunID != "" {
		t.Fatalf("charge = %+v, want no runner fields", charge)
	}
}

// The division truncates toward zero, so a fraction of a micro-credit is never
// billed: a gibibyte and a half for a day is 1,249,999 and not 1,250,000.
func TestStorageChargeTruncatesAFractionOfAMicroCredit(t *testing.T) {
	st := storetest.Open(t)
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, gib)
	start := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "r1", 2*gib+gib/2, start.Add(-time.Hour))

	billed := billOneInterval(t, st, start, 24*time.Hour)
	if len(billed.Charges) != 1 || billed.Charges[0].AmountMicro != 1_249_999 {
		t.Fatalf("charges = %+v, want one of 1249999 micro", billed.Charges)
	}
}

func TestStorageChargeBillsAPartialDayProRata(t *testing.T) {
	st := storetest.Open(t)
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, gib)
	start := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "r1", 3*gib, start.Add(-time.Hour))

	// safety: thirty hours is a day and a quarter, so 2 GiB at 833,333 a
	// gibibyte-day is 2,083,332.5 and the truncation bills 2,083,332.
	billed := billOneInterval(t, st, start, 30*time.Hour)
	if len(billed.Charges) != 1 || billed.Charges[0].AmountMicro != 2_083_332 {
		t.Fatalf("charges = %+v, want one of 2083332 micro", billed.Charges)
	}
	if billed.Charges[0].Seconds != 30*3600 {
		t.Fatalf("seconds = %d, want 108000", billed.Charges[0].Seconds)
	}
}

func TestStorageChargeWaitsForTheChargeInterval(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, gib)
	start := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "r1", 3*gib, start.Add(-time.Hour))

	billed := billOneInterval(t, st, start, 12*time.Hour)
	if len(billed.Charges) != 0 {
		t.Fatalf("a half day billed %+v, want nothing", billed.Charges)
	}
	// safety: the watermark did not move, so the whole day is billed at once.
	later, err := st.ChargeRetainedStorage(ctx, start.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("day pass: %v", err)
	}
	if len(later.Charges) != 1 || later.Charges[0].Seconds != 86_400 {
		t.Fatalf("charges = %+v, want one covering the whole day", later.Charges)
	}
}

func TestAnInstallThatSetsNothingWritesNoStorageCharge(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := st.SetStorageQuota(ctx, store.StorageQuota{
		Principal: "acme", MaxBytesPerRun: 1 << 40,
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	start := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "r1", 3*gib, start.Add(-time.Hour))

	for _, at := range []time.Time{start, start.Add(24 * time.Hour), start.Add(48 * time.Hour)} {
		billed, err := st.ChargeRetainedStorage(ctx, at)
		if err != nil {
			t.Fatalf("pass at %s: %v", at, err)
		}
		if len(billed.Charges) != 0 || billed.ChargedMicro != 0 {
			t.Fatalf("an install with no storage rate billed %+v", billed)
		}
	}
	charges, err := st.ListCreditCharges(ctx, 0)
	if err != nil {
		t.Fatalf("list charges: %v", err)
	}
	if len(charges) != 0 {
		t.Fatalf("charges = %+v, want none", charges)
	}
	state, err := st.CreditState(ctx, time.Hour)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.StorageRateMicroPerGBDay != 0 || state.StorageFreeAllowanceBytes != 0 ||
		state.StorageChargedMicro != 0 {
		t.Fatalf("state = %+v, want the storage settings at their defaults", state)
	}
}

func TestAMonthOfStorageChargesReconcilesWithTheBalance(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, gib)
	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 1_000*store.MicroCreditsPerCredit,
		"pay_1", "operator"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	start := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "r1", 3*gib, start.Add(-time.Hour))

	var billed int64
	for day := range 31 {
		pass, err := st.ChargeRetainedStorage(ctx, start.Add(time.Duration(day)*24*time.Hour))
		if err != nil {
			t.Fatalf("day %d: %v", day, err)
		}
		billed += pass.ChargedMicro
	}
	// safety: thirty billed days at two gibibytes is 25 credits a
	// gibibyte-month, which is the price this reconciles against.
	want := int64(30) * 2 * store.CloudStorageRateMicroPerGBDay
	if billed != want {
		t.Fatalf("billed %d micro over the month, want %d", billed, want)
	}
	state, err := st.CreditState(ctx, time.Hour)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.ReversedMicro != 0 {
		t.Fatalf("reversed = %d, want a reversal-free month", state.ReversedMicro)
	}
	if state.GrantedMicro-state.ChargedMicro != state.BalanceMicro {
		t.Fatalf("granted %d less charged %d is not the balance %d",
			state.GrantedMicro, state.ChargedMicro, state.BalanceMicro)
	}
	if state.StorageChargedMicro != want || state.ChargedMicro != want {
		t.Fatalf("charged %d of which %d storage, want both %d",
			state.ChargedMicro, state.StorageChargedMicro, want)
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
	var refusal *store.StorageCreditsExhaustedError
	if !errors.As(err, &refusal) || refusal.Principal != "acme" {
		t.Fatalf("refusal = %v, want one naming the team", err)
	}
	if got := countRows(t, st, `SELECT COUNT(*) FROM events`); got != 0 {
		t.Fatalf("events = %d, want the refused write stored nothing", got)
	}

	if _, err := st.GrantCredits(ctx, store.CreditGrantFree, 10*store.MicroCreditsPerCredit,
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
	if retained != int64(len("more bytes")) {
		t.Fatalf("retained = %d, want the payload just written", retained)
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
	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCredit,
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
