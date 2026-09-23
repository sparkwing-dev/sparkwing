package store_test

import (
	"context"
	"errors"
	"fmt"
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
// billed: a gibibyte and a half for a day is 499,999 and not 499,999.5.
func TestStorageChargeTruncatesAFractionOfAMicroCredit(t *testing.T) {
	st := storetest.Open(t)
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, gib)
	start := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "r1", 2*gib+gib/2, start.Add(-time.Hour))

	billed := billOneInterval(t, st, start, 24*time.Hour)
	if len(billed.Charges) != 1 || billed.Charges[0].AmountMicro != 499_999 {
		t.Fatalf("charges = %+v, want one of 499999 micro", billed.Charges)
	}
}

func TestStorageChargeBillsAPartialDayProRata(t *testing.T) {
	st := storetest.Open(t)
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, gib)
	start := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "r1", 3*gib, start.Add(-time.Hour))

	// safety: thirty hours is a day and a quarter, so 2 GiB at 333,333 a
	// gibibyte-day is 833,332.5 and the truncation bills 833,332.
	billed := billOneInterval(t, st, start, 30*time.Hour)
	if len(billed.Charges) != 1 || billed.Charges[0].AmountMicro != 833_332 {
		t.Fatalf("charges = %+v, want one of 833332 micro", billed.Charges)
	}
	if billed.Charges[0].Seconds != 30*3600 {
		t.Fatalf("seconds = %d, want 108000", billed.Charges[0].Seconds)
	}
}

// Every pass bills the interval it observes, so bytes held between two passes
// and released before the next are still paid for. Billing only once a day had
// elapsed made a day of held-then-released bytes free.
func TestStorageChargeBillsEveryPassSoReleasedBytesAreNotFree(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, 0)
	start := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "r1", 24*gib, start.Add(-time.Hour))

	billed := billOneInterval(t, st, start, time.Hour)
	if len(billed.Charges) != 1 || billed.Charges[0].AmountMicro != store.CloudStorageRateMicroPerGBDay {
		t.Fatalf("charges = %+v, want one hour of 24 GiB billed as a gibibyte-day", billed.Charges)
	}
	if billed.Charges[0].Seconds != 3600 {
		t.Fatalf("seconds = %d, want the hour the pass observed", billed.Charges[0].Seconds)
	}

	if _, err := st.DB().Exec(storetest.Rebind(st,
		`DELETE FROM storage_run_usage WHERE run_id = 'r1'`)); err != nil {
		t.Fatalf("release the bytes: %v", err)
	}
	after, err := st.ChargeRetainedStorage(ctx, start.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("pass after the release: %v", err)
	}
	if len(after.Charges) != 0 {
		t.Fatalf("charges = %+v, want nothing billed for bytes no longer held", after.Charges)
	}
	state, err := st.CreditState(ctx, time.Hour)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.StorageChargedMicro != store.CloudStorageRateMicroPerGBDay {
		t.Fatalf("storage billed %d, want the one hour the bytes were held",
			state.StorageChargedMicro)
	}
}

// The allowance is the most a team pays for, so bytes standing above it are
// never billed, even on the pass that is about to expire them.
// Two passes over one interval bill it once, which is what the watermark's
// compare-and-set gives two controllers sharing a database.
func TestTwoPassesOverOneIntervalBillItOnce(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, 0)
	start := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "r1", 2*gib, start.Add(-time.Hour))

	billed := billOneInterval(t, st, start, 24*time.Hour)
	if len(billed.Charges) != 1 {
		t.Fatalf("charges = %+v, want one", billed.Charges)
	}
	again, err := st.ChargeRetainedStorage(ctx, start.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(again.Charges) != 0 {
		t.Fatalf("charges = %+v, want the same interval billed once", again.Charges)
	}
	state, err := st.CreditState(ctx, time.Hour)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.StorageChargedMicro != 2*store.CloudStorageRateMicroPerGBDay {
		t.Fatalf("storage billed %d, want one day of two gibibytes",
			state.StorageChargedMicro)
	}
}

// A team that held nothing for weeks carries a watermark from before the gap.
// Billing the gap against the first fresh sample charged a team 503 times an
// honest hour, so the interval is clamped to how long the bytes billed have
// actually been held.
func TestAnIdleGapIsNotBilledWhenATeamStoresAgain(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, 0)
	start := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "r1", gib, start.Add(-time.Hour))

	hour := int64(store.CloudStorageRateMicroPerGBDay) / 24
	billed := billOneInterval(t, st, start, time.Hour)
	if len(billed.Charges) != 1 || billed.Charges[0].AmountMicro != hour {
		t.Fatalf("charges = %+v, want one gibibyte-hour of %d micro", billed.Charges, hour)
	}

	// safety: a cascading delete takes the bytes with no sweep behind them to
	// prune the watermark, which is the path the clamp has to cover.
	if _, err := st.DB().Exec(storetest.Rebind(st,
		`DELETE FROM storage_run_usage WHERE principal = 'acme'`)); err != nil {
		t.Fatalf("release the bytes: %v", err)
	}

	back := start.Add(21 * 24 * time.Hour)
	seedRetainedRun(t, st, "acme", "r2", gib, back.Add(-time.Hour))
	after, err := st.ChargeRetainedStorage(ctx, back)
	if err != nil {
		t.Fatalf("pass after the idle gap: %v", err)
	}
	if len(after.Charges) != 1 {
		t.Fatalf("charges = %+v, want one", after.Charges)
	}
	if got := after.Charges[0].AmountMicro; got > hour {
		t.Fatalf("billed %d micro after a three-week gap, want at most one gibibyte-hour of %d",
			got, hour)
	}
	if got := after.Charges[0].Seconds; got > 3600 {
		t.Fatalf("billed %ds, want at most the hour the bytes were held", got)
	}
}

// A team whose retained set is actively written holds its bytes for the whole
// interval, so it pays for the whole interval. Dating the bytes by their last
// write let one appended byte before each pass store 100 GiB for free.
func TestAnActivelyWrittenTeamIsBilledForTheWholeIntervalItHeld(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, 0)
	start := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "r1", 100*gib, start)

	if _, err := st.ChargeRetainedStorage(ctx, start); err != nil {
		t.Fatalf("stamp pass: %v", err)
	}
	var billed int64
	for hour := 1; hour <= 24; hour++ {
		at := start.Add(time.Duration(hour) * time.Hour)
		// safety: the run keeps writing, so its usage row is rewritten a
		// second before every pass; that must not redate the bytes it holds.
		if _, err := st.DB().Exec(storetest.Rebind(st,
			`UPDATE storage_run_usage SET updated_at = ? WHERE principal = 'acme'`),
			at.Add(-time.Second).UnixNano()); err != nil {
			t.Fatalf("touch the usage row: %v", err)
		}
		pass, err := st.ChargeRetainedStorage(ctx, at)
		if err != nil {
			t.Fatalf("pass at hour %d: %v", hour, err)
		}
		billed += pass.ChargedMicro
	}

	day := int64(100) * store.CloudStorageRateMicroPerGBDay
	onePass := day / 24
	if billed < day-onePass {
		t.Fatalf("billed %d micro for 100 GiB held a day, want within one pass of %d",
			billed, day)
	}
}

// A team holding nothing keeps no watermark, so nothing stale survives to
// price its next bytes and sparkwing_meta gains no key per team that ever
// stored.
func TestTheWatermarkIsGoneOnceATeamHoldsNothing(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, 0)
	if err := st.SetStorageSettings(ctx, store.StorageSettings{EventRetentionDays: 30}); err != nil {
		t.Fatalf("set retention: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "acme", "r1", gib, now.Add(-40*24*time.Hour))

	if _, err := st.ChargeRetainedStorage(ctx, now); err != nil {
		t.Fatalf("stamp pass: %v", err)
	}
	if got := storageWatermarkKeys(t, st); got != 1 {
		t.Fatalf("watermark keys = %d after a stamp, want 1", got)
	}
	swept, err := st.SweepStorageAllowance(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept.Runs != 1 {
		t.Fatalf("swept %+v, want the team drained", swept)
	}
	retained, err := st.StorageRetainedBytes(ctx, "acme")
	if err != nil {
		t.Fatalf("retained: %v", err)
	}
	if retained != 0 {
		t.Fatalf("retained = %d, want nothing held", retained)
	}
	if got := storageWatermarkKeys(t, st); got != 0 {
		t.Fatalf("watermark keys = %d after the drain, want none", got)
	}
}

func storageWatermarkKeys(t *testing.T, st *store.Store) int64 {
	t.Helper()
	var n int64
	if err := st.DB().QueryRow(storetest.Rebind(st,
		`SELECT COUNT(*) FROM sparkwing_meta WHERE key LIKE ?`),
		"storage_charged_through/%").Scan(&n); err != nil {
		t.Fatalf("count watermark keys: %v", err)
	}
	return n
}

func TestStorageChargeNeverBillsAboveTheAllowance(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	chargeableTeam(t, st, "acme", store.CloudStorageRateMicroPerGBDay, 0)
	if _, err := st.SetStorageAllowance(ctx, "acme", gib); err != nil {
		t.Fatalf("set allowance: %v", err)
	}
	start := time.Unix(1_700_000_000, 0).UTC()
	for i := range 100 {
		seedRetainedRun(t, st, "acme", fmt.Sprintf("r%d", i), gib,
			start.Add(-time.Duration(100-i)*time.Hour))
	}

	if _, err := st.ChargeRetainedStorage(ctx, start); err != nil {
		t.Fatalf("stamp pass: %v", err)
	}
	swept, err := st.SweepStorageAllowance(ctx, start.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept.Runs != 99 {
		t.Fatalf("swept %+v, want the 99 runs above the allowance", swept)
	}
	billed, err := st.ChargeRetainedStorage(ctx, start.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	if len(billed.Charges) != 1 || billed.Charges[0].StorageBytes != gib {
		t.Fatalf("charges = %+v, want the one gibibyte the team asked to keep", billed.Charges)
	}
	if billed.Charges[0].AmountMicro != store.CloudStorageRateMicroPerGBDay {
		t.Fatalf("amount = %d, want one gibibyte-day", billed.Charges[0].AmountMicro)
	}
}

// A team holding bytes without a quota row of its own is billed the same way,
// because bytes and not quota rows are what storage costs.
func TestStorageChargeBillsATeamWithNoQuotaRow(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	rate, free := int64(store.CloudStorageRateMicroPerGBDay), int64(0)
	if _, err := st.SetCreditSettings(ctx, store.CreditSettingsUpdate{
		StorageRateMicroPerGBDay: &rate, StorageFreeAllowanceBytes: &free,
	}); err != nil {
		t.Fatalf("set storage settings: %v", err)
	}
	start := time.Unix(1_700_000_000, 0).UTC()
	seedRetainedRun(t, st, "nomad", "r1", 2*gib, start.Add(-time.Hour))

	billed := billOneInterval(t, st, start, 24*time.Hour)
	if len(billed.Charges) != 1 || billed.Charges[0].Principal != "nomad" {
		t.Fatalf("charges = %+v, want one for the team with no quota row", billed.Charges)
	}
	if billed.Charges[0].AmountMicro != 2*store.CloudStorageRateMicroPerGBDay {
		t.Fatalf("amount = %d, want two gibibyte-days", billed.Charges[0].AmountMicro)
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
	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 1_000*store.MicroCreditsPerCent,
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
