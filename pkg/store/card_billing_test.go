package store_test

import (
	"context"
	"errors"
	"fmt"
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
