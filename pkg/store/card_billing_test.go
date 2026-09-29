package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func trustWithCard(t *testing.T, tenant *store.Tenant, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := tenant.SetBillingTrust(ctx, store.BillingTrustChange{
		Trust: store.BillingTrustGranted, Actor: "korey", Reason: "known customer",
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := tenant.SaveCard(ctx, store.Card{
		Customer: "cus_1", PaymentMethod: "pm_1", Fingerprint: "fp_1", Brand: "visa", Last4: "4242",
	}, "billing", now); err != nil {
		t.Fatal(err)
	}
}

func spentOn(t *testing.T, s *store.Store, team string, day time.Time, cents int64) {
	t.Helper()
	if _, err := s.DB().Exec(fmt.Sprintf(
		`INSERT INTO team_spend_days (team, day, amount_micro) VALUES ('%s', %d, %d)`,
		team, day.UnixNano()/int64(24*time.Hour), cents*store.MicroCreditsPerCent)); err != nil {
		t.Fatalf("seed spend: %v", err)
	}
}

func claimLimit(t *testing.T, s *store.Store, claimant store.ClaimIdentity) string {
	t.Helper()
	_, err := s.ClaimNextReadyNode(context.Background(), claimant, "pod-1", time.Minute, nil)
	var short *store.InsufficientCreditsError
	if err == nil {
		return ""
	}
	if !errors.As(err, &short) {
		t.Fatalf("claim: %v", err)
	}
	return short.Limit
}

// A New team's claim stops at its daily cap even with balance left, and the
// same spend on an earlier day leaves today's cap untouched.
func TestANewTeamStopsAtItsDailyCap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		day   time.Time
		limit string
	}{
		{"today", time.Now(), store.SpendLimitDailyCap},
		{"yesterday", time.Now().Add(-24 * time.Hour), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := storetest.Open(t)
			claimant, _ := fundedMeteredNode(t, s, "run-cap")
			if _, err := s.GrantCredits(context.Background(), store.CreditGrantPaid, dollarsMicro(30), "pay_more", "admin"); err != nil {
				t.Fatal(err)
			}
			spentOn(t, s, "default", tc.day, store.NewDailyCapCents)
			if got := claimLimit(t, s, claimant); got != tc.limit {
				t.Fatalf("claim refused by %q, want %q", got, tc.limit)
			}
		})
	}
}

// A trusted team with a card runs below zero on its card's credit; without
// the card, or once a charge fails, it stops at its balance.
func TestACardLetsATrustedTeamRunBelowZero(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-1", "build")
	def, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if got := claimLimit(t, s, claimant); got != store.SpendLimitBalance {
		t.Fatalf("an unfunded New team's claim refused by %q, want balance", got)
	}
	if _, _, err := def.SetBillingTrust(ctx, store.BillingTrustChange{
		Trust: store.BillingTrustGranted, Actor: "korey", Reason: "known",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := claimLimit(t, s, claimant); got != store.SpendLimitBalance {
		t.Fatalf("a trusted team without a card was refused by %q, want balance", got)
	}
	trustWithCard(t, def, time.Now())
	if got := claimLimit(t, s, claimant); got != "" {
		t.Fatalf("a trusted team with a card was refused by %q", got)
	}
	if bal, err := def.CreditBalanceMicro(ctx); err != nil || bal >= 0 {
		t.Fatalf("balance = %d, %v; want below zero", bal, err)
	}
}

// Debt past the rung opens one charge with one attempt, however often the
// worker asks; the payment settles it once, a repeat grants nothing, and the
// team's balance is whole again.
func TestADebtAtTheRungIsChargedOnce(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, now)
	spend(t, s, "acme", "ch_small", 5_000)
	if work, _, err := s.DueCardCharges(ctx, now); err != nil || len(work) != 0 {
		t.Fatalf("a $50 debt under the $100 rung = %+v, %v; want no charge", work, err)
	}
	spend(t, s, "acme", "ch_more", 7_050)

	work, _, err := s.DueCardCharges(ctx, now)
	if err != nil || len(work) != 1 || work[0].AmountCents != 12_050 || work[0].PaymentMethod != "pm_1" {
		t.Fatalf("work = %+v, %v; want one $120.50 charge to pm_1", work, err)
	}
	if again, _, err := s.DueCardCharges(ctx, now.Add(time.Second)); err != nil || len(again) != 0 {
		t.Fatalf("a second pass = %+v, %v; want the live attempt left alone", again, err)
	}
	if again, _, err := s.DueCardCharges(ctx, now.Add(3*time.Minute)); err != nil || len(again) != 1 ||
		again[0].AttemptID != work[0].AttemptID {
		t.Fatalf("a stalled attempt = %+v, %v; want it asked again under the same id", again, err)
	}

	pay := store.CardPayment{
		Team: "acme", ChargeID: work[0].ChargeID, AttemptID: work[0].AttemptID,
		PaymentIntent: "pi_card_1", AmountCents: 12_050,
	}
	if created, err := s.SettleCardPayment(ctx, pay, now); err != nil || !created {
		t.Fatalf("settle = %v, %v", created, err)
	}
	if created, err := s.SettleCardPayment(ctx, pay, now); err != nil || created {
		t.Fatalf("a repeated settle = %v, %v; want nothing written", created, err)
	}
	if bal, err := acme.CreditBalanceMicro(ctx); err != nil || bal != 0 {
		t.Fatalf("balance = %d, %v; want 0", bal, err)
	}
	st, err := acme.SpendStanding(ctx, now)
	if err != nil || st.OpenCharge != nil {
		t.Fatalf("standing = %+v, %v; want the charge closed", st, err)
	}
	other := pay
	other.Team = "globex"
	if _, err := s.SettleCardPayment(ctx, other, now); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("a payment naming another team = %v, want ErrInvalidInput", err)
	}
}

// A decline keeps the charge open, withdraws the card's credit and waits a
// day to retry; the owner's "pay now" opens a recovery attempt, and only one
// attempt is live at a time.
func TestADeclineHoldsTheTeamUntilPaid(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, now)
	spend(t, s, "acme", "ch_1", 15_000)
	work, _, err := s.DueCardCharges(ctx, now)
	if err != nil || len(work) != 1 {
		t.Fatalf("work = %+v, %v", work, err)
	}
	if _, err := acme.StartRecoveryAttempt(ctx, now); !errors.Is(err, store.ErrNoPayableCharge) {
		t.Fatalf("pay now during a live attempt = %v, want ErrNoPayableCharge", err)
	}
	team, first, err := s.FailCardAttempt(ctx, work[0].AttemptID, "pi_declined", "insufficient_funds", now)
	if err != nil || team != "acme" || !first {
		t.Fatalf("fail = %s %v %v", team, first, err)
	}
	st, err := acme.SpendStanding(ctx, now)
	if err != nil || !st.ChargeFailed() || st.CreditLimitMicro != 0 {
		t.Fatalf("standing = %+v, %v; want the card's credit withdrawn", st, err)
	}
	if _, limit := st.Headroom(); limit != store.SpendLimitChargeFailed {
		t.Fatalf("binding limit = %q, want charge_failed", limit)
	}
	if again, _, err := s.DueCardCharges(ctx, now.Add(23*time.Hour)); err != nil || len(again) != 0 {
		t.Fatalf("a retry before a day = %+v, %v", again, err)
	}
	if again, _, err := s.DueCardCharges(ctx, now.Add(25*time.Hour)); err != nil || len(again) != 1 {
		t.Fatalf("the retry after a day = %+v, %v", again, err)
	} else if _, _, err := s.FailCardAttempt(ctx, again[0].AttemptID, "pi_2", "insufficient_funds", now); err != nil {
		t.Fatal(err)
	}

	rec, err := acme.StartRecoveryAttempt(ctx, now)
	if err != nil || rec.ChargeID != work[0].ChargeID || rec.AmountCents != 15_000 {
		t.Fatalf("recovery = %+v, %v", rec, err)
	}
	if _, err := s.SettleCardPayment(ctx, store.CardPayment{
		ChargeID: rec.ChargeID, AttemptID: rec.AttemptID,
		PaymentIntent: "pi_paid", AmountCents: 15_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	if st, err := acme.SpendStanding(ctx, now); err != nil || st.ChargeFailed() || st.CreditLimitMicro == 0 {
		t.Fatalf("after paying = %+v, %v; want the card's credit back", st, err)
	}
}

// A held team's card is not charged, and revoking trust bills what is owed
// at once, under the rung.
func TestHoldsAndRevocation(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, now)
	spend(t, s, "acme", "ch_1", 2_000)
	if work, _, err := s.DueCardCharges(ctx, now); err != nil || len(work) != 0 {
		t.Fatalf("a $20 debt while trusted = %+v, %v; want none", work, err)
	}
	if _, _, err := acme.SetBillingTrust(ctx, store.BillingTrustChange{
		Trust: store.BillingTrustRevoked, Actor: "korey", Reason: "revoked",
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.HoldByOperator(ctx, "korey", "looking", now); err != nil {
		t.Fatal(err)
	}
	if work, _, err := s.DueCardCharges(ctx, now); err != nil || len(work) != 0 {
		t.Fatalf("a held team = %+v, %v; want no charge", work, err)
	}
	if _, err := acme.ReleaseOperatorHolds(ctx, "korey", "done", now); err != nil {
		t.Fatal(err)
	}
	work, _, err := s.DueCardCharges(ctx, now)
	if err != nil || len(work) != 1 || work[0].AmountCents != 2_000 {
		t.Fatalf("revoked with debt = %+v, %v; want the $20 charged", work, err)
	}
	if _, _, err := s.AsOperator().RequestTeamDeletion(ctx, "acme", now); !errors.Is(err, store.ErrTeamOwes) {
		t.Fatalf("deleting a team with a charge open = %v, want ErrTeamOwes", err)
	}
}

// A debt under the rung is billed on the first of the month after it began,
// and not before.
func TestMonthEndBillsASmallDebt(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	start := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, start)
	spend(t, s, "acme", "ch_1", 1_234)
	if work, _, err := s.DueCardCharges(ctx, start); err != nil || len(work) != 0 {
		t.Fatalf("mid-month = %+v, %v", work, err)
	}
	if work, _, err := s.DueCardCharges(ctx, start.Add(24*time.Hour)); err != nil || len(work) != 0 {
		t.Fatalf("the last day of the month = %+v, %v", work, err)
	}
	work, _, err := s.DueCardCharges(ctx, time.Date(2026, 10, 1, 0, 1, 0, 0, time.UTC))
	if err != nil || len(work) != 1 || work[0].AmountCents != 1_234 {
		t.Fatalf("the first of the month = %+v, %v; want $12.34", work, err)
	}
}

// A partial refund reverses exactly its amount, and the rest of the payment
// stays reversible.
func TestAPartialReversalTakesOnlyItsAmount(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	payTeam(t, acme, "pi_1", 25)
	first, err := s.ReversePayment(ctx, "pi_1", "re_1", "billing", dollarsMicro(10))
	if err != nil || first.Grant.AmountMicro != -dollarsMicro(10) {
		t.Fatalf("partial = %+v, %v", first, err)
	}
	rest, err := s.ReversePayment(ctx, "pi_1", "re_2", "billing", dollarsMicro(100))
	if err != nil || rest.Grant.AmountMicro != -dollarsMicro(15) || rest.ReversedMicro != dollarsMicro(25) {
		t.Fatalf("the rest = %+v, %v; want $15, capped at what is left", rest, err)
	}
}

// A budget reports each threshold once a month, and a team without a card
// never gets a charge opened, even owing money.
func TestABudgetAlertsOnceWithoutACard(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	if err := acme.SetSpendBudget(ctx, 2_000); err != nil {
		t.Fatal(err)
	}
	spend(t, s, "acme", "ch_1", 1_700)
	_, alerts, err := s.DueCardCharges(ctx, now)
	if err != nil || len(alerts) != 1 || alerts[0].Percent != 80 {
		t.Fatalf("alerts = %+v, %v; want the 80%% alert", alerts, err)
	}
	if work, again, err := s.DueCardCharges(ctx, now); err != nil || len(again) != 0 || len(work) != 0 {
		t.Fatalf("a second pass = %+v %+v, %v; want nothing", work, again, err)
	}
	if st, err := acme.SpendStanding(ctx, now); err != nil || st.OpenCharge != nil {
		t.Fatalf("a team without a card got a charge: %+v, %v", st.OpenCharge, err)
	}
}

func dueCharge(t *testing.T, s *store.Store, now time.Time) store.CardChargeWork {
	t.Helper()
	work, _, err := s.DueCardCharges(context.Background(), now)
	if err != nil || len(work) != 1 {
		t.Fatalf("work = %+v, %v; want one charge", work, err)
	}
	return work[0]
}

// A warning that reaches the controller before its payment settles is kept,
// and the payment is then neither granted nor counted as paid: the team is
// held for the warning and the charge stays open.
func TestAWarningBeforeItsPaymentStopsTheGrant(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, now)
	spend(t, s, "acme", "ch_1", 15_000)
	w := dueCharge(t, s, now)
	team, err := s.RecordPaymentWarning(ctx, store.PaymentWarning{
		WarningID: "issfr_1", PaymentIntent: "pi_warned", Fingerprint: "fp_other", Actionable: true,
	}, now)
	if err != nil || team != "" {
		t.Fatalf("warning on an unknown payment = %q, %v; want stored with no team", team, err)
	}
	created, err := s.SettleCardPayment(ctx, store.CardPayment{
		Team: "acme", ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_warned", AmountCents: 15_000,
	}, now)
	if err != nil || created {
		t.Fatalf("settling a warned payment = %v, %v; want nothing granted", created, err)
	}
	if bal, err := acme.CreditBalanceMicro(ctx); err != nil || bal != -15_000*store.MicroCreditsPerCent {
		t.Fatalf("balance = %d, %v; want the debt still owed", bal, err)
	}
	freeze, err := acme.CreditFreeze(ctx)
	if err != nil || !freeze.Frozen || freeze.Disputes[0] != "issfr_1" {
		t.Fatalf("freeze = %+v, %v; want the team held for the warning", freeze, err)
	}
	st, err := acme.SpendStanding(ctx, now)
	if err != nil || st.OpenCharge == nil || st.Billing.Trusted {
		t.Fatalf("standing = %+v, %v; want the charge open and the team back at New", st, err)
	}
}

// A prepaid purchase whose payment drew a warning is refused at the ledger.
func TestAWarnedPurchaseIsNotGranted(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	if _, err := s.RecordPaymentWarning(ctx, store.PaymentWarning{WarningID: "issfr_2", PaymentIntent: "pi_buy", Actionable: true},
		time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.GrantCredits(ctx, store.CreditGrantPaid, 10_000*store.MicroCreditsPerCredit, "pi_buy", "billing"); !errors.Is(err, store.ErrPaymentWarned) {
		t.Fatalf("grant of a warned payment = %v, want ErrPaymentWarned", err)
	}
}

// A decline holds the team, drops an operator's grant back to New and stops
// every claim, prepaid balance or not; the retries still run under the hold,
// and paying the charge lifts it.
func TestADeclineHoldsATeamWithPrepaidBalanceAtNew(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, now)
	spend(t, s, "acme", "ch_1", 15_000)
	w := dueCharge(t, s, now)
	if _, _, err := s.FailCardAttempt(ctx, w.AttemptID, "pi_declined", "card_declined", now); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.GrantCredits(ctx, store.CreditGrantFree, 400*store.MicroCreditsPerCredit*1000, "promo", "admin"); err != nil {
		t.Fatal(err)
	}
	if b, err := acme.BillingStanding(ctx, now); err != nil || b.Trusted {
		t.Fatalf("standing = %+v, %v; want the granted team back at New while held", b, err)
	}
	claimant := meteredTeamClaimant(t, acme, "agent:cloud")
	readyTeamNode(t, s, acme, "run-held", "build")
	if limit := claimLimit(t, s, claimant); limit != store.SpendLimitChargeFailed {
		t.Fatalf("claim while held = %q, want refused as a failed charge", limit)
	}
	retry := dueCharge(t, s, now.Add(25*time.Hour))
	if _, err := s.SettleCardPayment(ctx, store.CardPayment{
		ChargeID: retry.ChargeID, AttemptID: retry.AttemptID, PaymentIntent: "pi_retry", AmountCents: 15_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	if freeze, err := acme.CreditFreeze(ctx); err != nil || freeze.Frozen {
		t.Fatalf("freeze after paying = %+v, %v; want released", freeze, err)
	}
	if b, err := acme.BillingStanding(ctx, now); err != nil || !b.Trusted {
		t.Fatalf("standing after paying = %+v, %v; want trusted again", b, err)
	}
}

// One card's spend counts against its limits across every team it pays for:
// a claim by one team writes the card's bucket, and the card's cap binds the
// other team.
func TestOneCardSpendsItsLimitsOnceAcrossTeams(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	globex := teamHandle(t, s, "globex")
	trustWithCard(t, acme, now)
	trustWithCard(t, globex, now)
	readyTeamNode(t, s, acme, "run-acme", "build")
	if _, err := s.ClaimNextReadyNode(ctx, meteredTeamClaimant(t, acme, "agent:cloud"), "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("acme's claim: %v", err)
	}
	st, err := globex.SpendStanding(ctx, now)
	if err != nil || st.CardSpentTodayMicro <= 0 || st.SpentTodayMicro != 0 {
		t.Fatalf("globex standing = %+v, %v; want acme's reservation on the shared card only", st, err)
	}
	if _, err := s.DB().Exec(storetest.Rebind(s, `UPDATE card_spend_days SET amount_micro = ? WHERE fingerprint = 'fp_1'`),
		store.TrustedDailyCapCents*store.MicroCreditsPerCent); err != nil {
		t.Fatal(err)
	}
	if st, err = globex.SpendStanding(ctx, now); err != nil {
		t.Fatal(err)
	}
	if room, limit := st.Headroom(); room > 0 || limit != store.SpendLimitCardDailyCap {
		t.Fatalf("globex headroom = %d %q; want the shared card's daily cap spent", room, limit)
	}
}

// Settlement pays a charge only through its own live attempt at the frozen
// amount. Money that arrives any other way is granted and queued for a
// refund, and an attempt of another charge is refused.
func TestSettlementChecksTheAttemptAndTheFrozenAmount(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, now)
	spend(t, s, "acme", "ch_1", 15_000)
	w := dueCharge(t, s, now)
	if _, err := s.SettleCardPayment(ctx, store.CardPayment{
		ChargeID: "cardcharge-other", AttemptID: w.AttemptID, PaymentIntent: "pi_x", AmountCents: 15_000,
	}, now); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("an attempt settling another charge = %v, want ErrInvalidInput", err)
	}
	if _, err := s.SettleCardPayment(ctx, store.CardPayment{
		ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_short", AmountCents: 100,
	}, now); err != nil {
		t.Fatal(err)
	}
	if st, err := acme.SpendStanding(ctx, now); err != nil || st.OpenCharge == nil {
		t.Fatalf("standing = %+v, %v; want a short payment to leave the charge open", st, err)
	}
	if err := s.RecordAttemptIntent(ctx, w.AttemptID, "pi_full", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleCardPayment(ctx, store.CardPayment{
		ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_full", AmountCents: 15_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleCardPayment(ctx, store.CardPayment{
		ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_second", AmountCents: 15_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	due, err := s.DueCardRefunds(ctx, now)
	if err != nil || len(due) != 2 || due[0].PaymentIntent != "pi_short" || due[0].Reason != "amount_mismatch" ||
		due[1].PaymentIntent != "pi_second" || due[1].Reason != "duplicate" {
		t.Fatalf("queued refunds = %+v, %v; want the short and the second payment", due, err)
	}
	if err := s.MarkCardRefundMade(ctx, "acme", "pi_short", "re_1", "pending", now); err != nil {
		t.Fatal(err)
	}
	if due, err := s.DueCardRefunds(ctx, now); err != nil || len(due) != 1 {
		t.Fatalf("queued refunds after one was made = %+v, %v", due, err)
	}
}

// An attempt that never recorded its payment is replaced once Stripe may
// have forgotten its key; one that recorded it keeps its id and payment.
func TestAnAttemptPastTheKeyLifeIsReplaced(t *testing.T) {
	s := storetest.Open(t)
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, now)
	spend(t, s, "acme", "ch_1", 15_000)
	first := dueCharge(t, s, now)
	next := dueCharge(t, s, now.Add(24*time.Hour))
	if next.AttemptID == first.AttemptID || next.PaymentIntent != "" {
		t.Fatalf("attempt after a day = %+v; want a new attempt, not the old key", next)
	}
	if err := s.RecordAttemptIntent(context.Background(), next.AttemptID, "pi_bound", now); err != nil {
		t.Fatal(err)
	}
	later := dueCharge(t, s, now.Add(72*time.Hour))
	if later.AttemptID != next.AttemptID || later.PaymentIntent != "pi_bound" {
		t.Fatalf("attempt with a recorded payment = %+v; want it read by that payment", later)
	}
}

// The v87 backfill counts a refund on the day of the reservation it refunds:
// the node's latest reservation before it, not its first, as new writes do.
func TestTheSpendBackfillCountsARefundOnItsReservationDay(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	day := int64(24 * time.Hour)
	today := time.Now().UnixNano() / day
	for _, row := range []struct {
		id, kind string
		amount   int64
		day      int64
	}{
		{"c_first", "reservation", 700, today - 3},
		{"c_second", "reservation", 500, today - 1},
		{"c_ref", "refund", -400, today},
	} {
		if _, err := st.DB().Exec(storetest.Rebind(st, `INSERT INTO credit_charges (id, run_id, node_id, token_prefix,
		    kind, seconds, amount_micro, charged_at, team) VALUES (?, 'run-b', 'build', 'pfx', ?, 60, ?, ?, 'default')`),
			row.id, row.kind, row.amount, row.day*day+1); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{`DELETE FROM team_spend_days`, `DELETE FROM sparkwing_schema_version WHERE version >= 87`} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("rerun v87: %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	got := map[int64]int64{}
	rows, err := upgraded.DB().Query(`SELECT day, amount_micro FROM team_spend_days WHERE team = 'default'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var d, amount int64
		if err := rows.Scan(&d, &amount); err != nil {
			t.Fatal(err)
		}
		got[d] = amount
	}
	if len(got) != 2 || got[today-3] != 700 || got[today-1] != 100 {
		t.Fatalf("buckets = %v; want the refund netted on the second reservation's day", got)
	}
}

// safety: checkout, card saving, trust changes, the billing pass, settlement
// and grants all touch the ledger and the team row; on Postgres any two that
// took them in opposite orders deadlock, which this surfaces as an error.
func TestBillingWritersShareOneLockOrder(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, now)
	const rounds = 15
	var mu sync.Mutex
	var deadlocks []error
	note := func(err error) {
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "deadlock") {
			mu.Lock()
			deadlocks = append(deadlocks, err)
			mu.Unlock()
		}
	}
	writers := []func(i int){
		func(i int) {
			_, err := acme.OpenCreditCheckout(ctx, 10*store.MicroCreditsPerCredit, now, time.Hour)
			note(err)
		},
		func(i int) {
			note(acme.SaveCard(ctx, store.Card{Customer: "cus_1", PaymentMethod: "pm_1", Fingerprint: "fp_1"}, "billing", now))
		},
		func(i int) {
			_, _, err := acme.SetBillingTrust(ctx, store.BillingTrustChange{
				Trust: store.BillingTrustGranted, Actor: "korey", Reason: "race",
			}, now)
			note(err)
		},
		func(i int) {
			work, _, err := s.DueCardCharges(ctx, now.Add(time.Duration(i)*time.Hour))
			note(err)
			for _, w := range work {
				_, err := s.SettleCardPayment(ctx, store.CardPayment{
					ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: fmt.Sprintf("pi_race_%d", i),
					AmountCents: w.AmountCents,
				}, now)
				note(err)
			}
		},
		func(i int) {
			_, err := s.DB().Exec(storetest.Rebind(s, `INSERT INTO team_spend_days (team, day, amount_micro)
			    VALUES ('acme', ?, 1) ON CONFLICT (team, day) DO UPDATE SET amount_micro = team_spend_days.amount_micro + 1`),
				now.UnixNano()/int64(24*time.Hour))
			note(err)
			_, err = acme.GrantCredits(ctx, store.CreditGrantFree, store.MicroCreditsPerCredit, fmt.Sprintf("promo_%d", i), "admin")
			note(err)
		},
	}
	var wg sync.WaitGroup
	for _, write := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range rounds {
				write(i)
			}
		}()
	}
	wg.Wait()
	if len(deadlocks) > 0 {
		t.Fatalf("%d writers deadlocked; first: %v", len(deadlocks), deadlocks[0])
	}
}

// A non-actionable warning is recorded but stops no grant and holds no team,
// because nothing refunds its payment.
func TestANonActionableWarningStopsNoGrant(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, now)
	spend(t, s, "acme", "ch_1", 15_000)
	w := dueCharge(t, s, now)
	if err := s.RecordAttemptIntent(ctx, w.AttemptID, "pi_quiet", now); err != nil {
		t.Fatal(err)
	}
	if team, err := s.RecordPaymentWarning(ctx, store.PaymentWarning{
		WarningID: "issfr_quiet", PaymentIntent: "pi_quiet", Fingerprint: "fp_1",
	}, now); err != nil || team != "" {
		t.Fatalf("non-actionable warning = %q, %v; want recorded with no hold", team, err)
	}
	if created, err := s.SettleCardPayment(ctx, store.CardPayment{
		ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_quiet", AmountCents: 15_000, Fingerprint: "fp_1",
	}, now); err != nil || !created {
		t.Fatalf("settle = %v, %v; want the payment granted", created, err)
	}
	if freeze, err := acme.CreditFreeze(ctx); err != nil || freeze.Frozen {
		t.Fatalf("freeze = %+v, %v; want none", freeze, err)
	}
	if _, err := s.RecordPaymentWarning(ctx, store.PaymentWarning{WarningID: "issfr_q2", PaymentIntent: "pi_buy"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.GrantCredits(ctx, store.CreditGrantPaid, store.MicroCreditsPerCredit, "pi_buy", "billing"); err != nil {
		t.Fatalf("a purchase with a non-actionable warning = %v; want granted", err)
	}
}

// A warning on the card on file does not stop a pay-now payment made with
// another card, and a warning on the card that paid does.
func TestAWarningIsJudgedByTheCardThatPaid(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, now)
	spend(t, s, "acme", "ch_1", 15_000)
	w := dueCharge(t, s, now)
	if _, _, err := s.FailCardAttempt(ctx, w.AttemptID, "pi_dead", "card_declined", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordPaymentWarning(ctx, store.PaymentWarning{
		WarningID: "issfr_saved", PaymentIntent: "pi_old", Fingerprint: "fp_1", Actionable: true,
	}, now); err != nil {
		t.Fatal(err)
	}
	rec, err := acme.StartRecoveryAttempt(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := s.SettleCardPayment(ctx, store.CardPayment{
		ChargeID: rec.ChargeID, AttemptID: rec.AttemptID, PaymentIntent: "pi_other_card", AmountCents: 15_000,
		Fingerprint: "fp_other",
	}, now); err != nil || !created {
		t.Fatalf("pay-now with an unwarned card = %v, %v; want granted", created, err)
	}
	if bal, err := acme.CreditBalanceMicro(ctx); err != nil || bal != 0 {
		t.Fatalf("balance = %d, %v; want the debt paid", bal, err)
	}
	spend(t, s, "acme", "ch_2", 15_000)
	next := dueCharge(t, s, now.Add(time.Hour))
	if created, err := s.SettleCardPayment(ctx, store.CardPayment{
		ChargeID: next.ChargeID, AttemptID: next.AttemptID, PaymentIntent: "pi_saved_card", AmountCents: 15_000,
		Fingerprint: "fp_1",
	}, now); err != nil || created {
		t.Fatalf("a payment by the warned card = %v, %v; want refused", created, err)
	}
}

// A queued refund is done only when it succeeds; a failed one is made again
// under a new key after a backoff, and a stale report of an old refund
// changes nothing.
func TestAFailedQueuedRefundIsRetried(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, now)
	spend(t, s, "acme", "ch_1", 15_000)
	w := dueCharge(t, s, now)
	if _, err := s.SettleCardPayment(ctx, store.CardPayment{
		ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_short", AmountCents: 100,
	}, now); err != nil {
		t.Fatal(err)
	}
	due, err := s.DueCardRefunds(ctx, now)
	if err != nil || len(due) != 1 || due[0].Key != "pi_short-0" {
		t.Fatalf("due = %+v, %v", due, err)
	}
	if err := s.MarkCardRefundMade(ctx, "acme", "pi_short", "re_1", "pending", now); err != nil {
		t.Fatal(err)
	}
	if due, err := s.DueCardRefunds(ctx, now); err != nil || len(due) != 0 {
		t.Fatalf("a pending refund = %+v, %v; want not due", due, err)
	}
	if queued, err := s.ReportCardRefund(ctx, "pi_short", "re_1", "failed", now); err != nil || !queued {
		t.Fatalf("report failed = %v, %v", queued, err)
	}
	if due, err := s.DueCardRefunds(ctx, now.Add(30*time.Minute)); err != nil || len(due) != 0 {
		t.Fatalf("a failed refund inside its backoff = %+v, %v", due, err)
	}
	due, err = s.DueCardRefunds(ctx, now.Add(2*time.Hour))
	if err != nil || len(due) != 1 || due[0].Key != "pi_short-1" {
		t.Fatalf("a failed refund after its backoff = %+v, %v; want it due under a new key", due, err)
	}
	if err := s.MarkCardRefundMade(ctx, "acme", "pi_short", "re_2", "succeeded", now); err != nil {
		t.Fatal(err)
	}
	if queued, err := s.ReportCardRefund(ctx, "pi_short", "re_1", "failed", now); err != nil || queued {
		t.Fatalf("a stale report of the first refund = %v, %v; want ignored", queued, err)
	}
	if due, err := s.DueCardRefunds(ctx, now.Add(48*time.Hour)); err != nil || len(due) != 0 {
		t.Fatalf("a succeeded refund = %+v, %v; want done", due, err)
	}
}

// With no guard set, a team still holds at most the default Cloud nodes, and
// an operator can raise one granted team's cap.
func TestTheTeamRunnerCapDefaultsAndIsRaisedPerTeam(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	claimant := meteredTeamClaimant(t, acme, "agent:cloud")
	if _, err := acme.GrantCredits(ctx, store.CreditGrantPaid, 1000*store.MicroCreditsPerCredit, "pay_acme", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(storetest.Rebind(s, `UPDATE teams SET billing_runner_cap = 0 WHERE name = 'acme'`)); err != nil {
		t.Fatal(err)
	}
	if err := acme.CreateRun(ctx, store.Run{ID: "held", Pipeline: "demo", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i := range store.DefaultTeamCloudNodes {
		if err := s.CreateNode(ctx, store.Node{RunID: "held", NodeID: fmt.Sprintf("n%d", i), Status: "running"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec(`UPDATE nodes SET credit_charged_through = 1 WHERE run_id = 'held'`); err != nil {
		t.Fatalf("hold the seeded nodes: %v", err)
	}
	readyTeamNode(t, s, acme, "run-next", "build")
	_, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
	refused := limitRefusal(t, err, store.ComputeLimitConcurrentRunners)
	if refused.Cap != store.DefaultTeamCloudNodes {
		t.Fatalf("refusal = %+v, want the default cap", refused)
	}
	if _, _, err := acme.SetBillingTrust(ctx, store.BillingTrustChange{
		Trust: store.BillingTrustGranted, Actor: "korey", Reason: "big build farm", RunnerCap: 200,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-2", time.Minute, nil); err != nil {
		t.Fatalf("claim under a raised cap: %v", err)
	}
}

// A refund debits the card that paid the reservation it refunds, even once
// the team has moved to another card.
func TestARefundDebitsTheCardThatPaid(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, now)
	claimant := meteredTeamClaimant(t, acme, "agent:cloud")
	readyTeamNode(t, s, acme, "run-card", "build")
	if _, err := s.ClaimNamedNode(ctx, claimant, "run-card", "build", "pod-1", time.Minute,
		store.NamedClaimOptions{SizesToClass: true}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := acme.SaveCard(ctx, store.Card{Customer: "cus_1", PaymentMethod: "pm_2", Fingerprint: "fp_2"}, "billing", now); err != nil {
		t.Fatal(err)
	}
	res, err := s.FinalizeNodeCredits(ctx, "run-card", "build", claimant.TokenPrefix, time.Now().Add(15*time.Minute))
	if err != nil || res.Charge == nil || res.Charge.Kind != store.CreditChargeRefund {
		t.Fatalf("finalize = %+v, %v; want the reservation refunded", res, err)
	}
	var old, replacement int64
	for fp, into := range map[string]*int64{"fp_1": &old, "fp_2": &replacement} {
		if err := s.DB().QueryRow(storetest.Rebind(s, `SELECT COALESCE(SUM(amount_micro), 0) FROM card_spend_days
		    WHERE fingerprint = ?`), fp).Scan(into); err != nil {
			t.Fatal(err)
		}
	}
	if old != 0 || replacement != 0 {
		t.Fatalf("buckets after the refund: first card %d, replacement %d; want the refund netted on the first", old, replacement)
	}
}
