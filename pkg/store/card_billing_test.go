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
	if err := tenant.TestOnlySaveCard(ctx, store.Card{
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

func TestHoldsAndRevocation(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, now)
	spend(t, s, "acme", "ch_1", 200)
	if work, _, err := s.AsOperator().DueCardCharges(ctx, now); err != nil || len(work) != 0 {
		t.Fatalf("a $2 debt while trusted = %+v, %v; want none", work, err)
	}
	if _, _, err := acme.SetBillingTrust(ctx, store.BillingTrustChange{
		Trust: store.BillingTrustRevoked, Actor: "korey", Reason: "revoked",
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.HoldByOperator(ctx, "korey", "looking", now); err != nil {
		t.Fatal(err)
	}
	if work, _, err := s.AsOperator().DueCardCharges(ctx, now); err != nil || len(work) != 0 {
		t.Fatalf("a held team = %+v, %v; want no charge", work, err)
	}
	if _, err := acme.ReleaseOperatorHolds(ctx, "korey", "done", now); err != nil {
		t.Fatal(err)
	}
	work, _, err := s.AsOperator().DueCardCharges(ctx, now.AddDate(0, 1, 0))
	if err != nil || len(work) != 1 || work[0].AmountCents != 200 {
		t.Fatalf("revoked with debt = %+v, %v; want the $2 charged", work, err)
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
	spend(t, s, "acme", "ch_1", 123)
	if work, _, err := s.AsOperator().DueCardCharges(ctx, start); err != nil || len(work) != 0 {
		t.Fatalf("mid-month = %+v, %v", work, err)
	}
	if work, _, err := s.AsOperator().DueCardCharges(ctx, start.Add(24*time.Hour)); err != nil || len(work) != 0 {
		t.Fatalf("the last day of the month = %+v, %v", work, err)
	}
	work, _, err := s.AsOperator().DueCardCharges(ctx, time.Date(2026, 10, 1, 0, 1, 0, 0, time.UTC))
	if err != nil || len(work) != 1 || work[0].AmountCents != 123 {
		t.Fatalf("the first of the month = %+v, %v; want $1.23", work, err)
	}
}

// A partial refund reverses exactly its amount, and the rest of the payment
// stays reversible.
func TestAPartialReversalTakesOnlyItsAmount(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	payTeam(t, acme, "pi_1", 25)
	first, err := s.AsOperator().TestOnlyReversePayment(ctx, "pi_1", "re_1", "billing", dollarsMicro(10))
	if err != nil || first.Grant.AmountMicro != -dollarsMicro(10) {
		t.Fatalf("partial = %+v, %v", first, err)
	}
	rest, err := s.AsOperator().TestOnlyReversePayment(ctx, "pi_1", "re_2", "billing", dollarsMicro(100))
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
	_, alerts, err := s.AsOperator().DueCardCharges(ctx, now)
	if err != nil || len(alerts) != 1 || alerts[0].Percent != 80 {
		t.Fatalf("alerts = %+v, %v; want the 80%% alert", alerts, err)
	}
	if work, again, err := s.AsOperator().DueCardCharges(ctx, now); err != nil || len(again) != 0 || len(work) != 0 {
		t.Fatalf("a second pass = %+v %+v, %v; want nothing", work, again, err)
	}
	if st, err := acme.SpendStanding(ctx, now); err != nil || st.OpenCharge != nil {
		t.Fatalf("a team without a card got a charge: %+v, %v", st.OpenCharge, err)
	}
}

func dueCharge(t *testing.T, s *store.Store, now time.Time) store.CardChargeWork {
	t.Helper()
	work, _, err := s.AsOperator().DueCardCharges(context.Background(), now)
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
	team, err := s.AsOperator().TestOnlyRecordPaymentWarning(ctx, store.PaymentWarning{
		WarningID: "issfr_1", PaymentIntent: "pi_warned", Fingerprint: "fp_other", Actionable: true,
	}, now)
	if err != nil || team != "" {
		t.Fatalf("warning on an unknown payment = %q, %v; want stored with no team", team, err)
	}
	created, _, err := s.AsOperator().SettleCardPayment(ctx, store.CardPayment{
		Team: "acme", ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_warned", AmountCents: 15_000,
	}, now)
	if err != nil || created {
		t.Fatalf("settling a warned payment = %v, %v; want nothing granted", created, err)
	}
	if bal, err := acme.CreditBalanceMicro(ctx); err != nil || bal != -15_000*store.MicroCreditsPerCent {
		t.Fatalf("balance = %d, %v; want the debt still owed", bal, err)
	}
	freeze, err := acme.CreditFreeze(ctx)
	if err != nil || !freeze.Frozen || len(freeze.Disputes) != 1 {
		t.Fatalf("freeze = %+v, %v; want the team held for the warning", freeze, err)
	}
	st, err := acme.SpendStanding(ctx, now)
	if err != nil || st.OpenCharge == nil || st.Billing.Trusted {
		t.Fatalf("standing = %+v, %v; want the charge open and the team back at New", st, err)
	}
}

// A pay-now payment by a card warned only on another payment repays exactly
// the debt and holds the team for review, and names the warning for the
// operator's alert; it queues no refund. The same payment of the wrong amount,
// or a payment the warning names itself, still grants nothing.
func TestAWarnedCardRepaysTheDebtOnPayNow(t *testing.T) {
	now := time.Now()
	recovery := func(t *testing.T, warnedIntent string) (*store.Store, *store.Tenant, store.CardChargeWork) {
		s := storetest.Open(t)
		acme := teamHandle(t, s, "acme")
		trustWithCard(t, acme, now)
		spend(t, s, "acme", "ch_1", 15_000)
		w := dueCharge(t, s, now)
		ctx := context.Background()
		if _, _, err := s.AsOperator().FailCardAttempt(ctx, w.AttemptID, "pi_declined", "card_declined", now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(ctx, store.PaymentWarning{
			WarningID: "issfr_elsewhere", PaymentIntent: warnedIntent, Fingerprint: "fp_flagged", Actionable: true,
		}, now); err != nil {
			t.Fatal(err)
		}
		rec, err := acme.StartRecoveryAttempt(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		return s, acme, rec
	}
	count := func(t *testing.T, s *store.Store, query string, args ...any) int {
		var n int
		if err := s.DB().QueryRow(storetest.Rebind(s, query), args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}

	t.Run("repays", func(t *testing.T) {
		s, acme, rec := recovery(t, "pi_elsewhere")
		ctx := context.Background()
		pay := store.CardPayment{
			Team: "acme", ChargeID: rec.ChargeID, AttemptID: rec.AttemptID,
			PaymentIntent: "pi_recovery", AmountCents: rec.AmountCents, Fingerprint: "fp_flagged",
		}
		created, warned, err := s.AsOperator().SettleCardPayment(ctx, pay, now)
		if err != nil || !created || warned == nil || warned.WarningID != "issfr_elsewhere" ||
			warned.RepaidMicro != 15_000*store.MicroCreditsPerCent || warned.UnappliedMicro != 0 {
			t.Fatalf("settle = %v, %+v, %v; want the debt repaid and the warning named", created, warned, err)
		}
		if bal, err := acme.CreditBalanceMicro(ctx); err != nil || bal != 0 {
			t.Fatalf("balance = %d, %v; want the debt repaid and nothing beyond it", bal, err)
		}
		if n := count(t, s, `SELECT COUNT(*) FROM card_refunds WHERE payment_intent = 'pi_recovery'`); n != 0 {
			t.Fatalf("queued refunds = %d, want none", n)
		}
		if n := count(t, s, `SELECT COUNT(*) FROM card_charges WHERE id = ? AND state = 'paid'`, rec.ChargeID); n != 1 {
			t.Fatalf("charge not paid")
		}
		if freeze, err := acme.CreditFreeze(ctx); err != nil || !freeze.Frozen {
			t.Fatalf("freeze = %+v, %v; want the team held for review", freeze, err)
		}
		if created, warned, err := s.AsOperator().SettleCardPayment(ctx, pay, now); err != nil || created || warned != nil {
			t.Fatalf("redelivery = %v, %+v, %v; want a silent no-op", created, warned, err)
		}
	})
	t.Run("wrong amount", func(t *testing.T) {
		s, acme, rec := recovery(t, "pi_elsewhere")
		ctx := context.Background()
		created, warned, err := s.AsOperator().SettleCardPayment(ctx, store.CardPayment{
			Team: "acme", ChargeID: rec.ChargeID,
			AttemptID: rec.AttemptID, PaymentIntent: "pi_recovery", AmountCents: rec.AmountCents + 1, Fingerprint: "fp_flagged",
		}, now)
		if err != nil || created || warned == nil || warned.WarningID != "issfr_elsewhere" || warned.RepaidMicro != 0 {
			t.Fatalf("settle = %v, %+v, %v; want nothing granted and the warning named", created, warned, err)
		}
		if bal, err := acme.CreditBalanceMicro(ctx); err != nil || bal != -15_000*store.MicroCreditsPerCent {
			t.Fatalf("balance = %d, %v; want the debt still owed", bal, err)
		}
	})
	t.Run("warned payment", func(t *testing.T) {
		s, acme, rec := recovery(t, "pi_recovery")
		ctx := context.Background()
		created, warned, err := s.AsOperator().SettleCardPayment(ctx, store.CardPayment{
			Team: "acme", ChargeID: rec.ChargeID,
			AttemptID: rec.AttemptID, PaymentIntent: "pi_recovery", AmountCents: rec.AmountCents, Fingerprint: "fp_flagged",
		}, now)
		if err != nil || created || warned == nil || warned.WarningID != "issfr_elsewhere" {
			t.Fatalf("settle = %v, %+v, %v; want nothing granted", created, warned, err)
		}
		if bal, err := acme.CreditBalanceMicro(ctx); err != nil || bal != -15_000*store.MicroCreditsPerCent {
			t.Fatalf("balance = %d, %v; want the debt still owed", bal, err)
		}
	})
	for _, tc := range []struct {
		name              string
		refundedCents     int64
		repaid, unapplied int64
		created           bool
	}{
		{"reservation refunded in full", 15_000, 0, 15_000, true},
		{"reservation refunded in part", 5_000, 10_000, 5_000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, acme, rec := recovery(t, "pi_elsewhere")
			if _, err := s.DB().Exec(fmt.Sprintf(
				`INSERT INTO credit_charges (id, run_id, node_id, capability_prefix, kind, seconds, amount_micro, charged_at, team)
				 VALUES ('refund_1', 'run-x', 'build', 'pfx', '%s', -60, %d, %d, 'acme')`,
				store.CreditChargeRefund, -tc.refundedCents*store.MicroCreditsPerCent, now.UnixNano())); err != nil {
				t.Fatalf("seed a reservation refund: %v", err)
			}
			ctx := context.Background()
			created, warned, err := s.AsOperator().SettleCardPayment(ctx, store.CardPayment{
				Team: "acme", ChargeID: rec.ChargeID, AttemptID: rec.AttemptID,
				PaymentIntent: "pi_recovery", AmountCents: rec.AmountCents, Fingerprint: "fp_flagged",
			}, now)
			if err != nil || created != tc.created || warned == nil ||
				warned.RepaidMicro != tc.repaid*store.MicroCreditsPerCent ||
				warned.UnappliedMicro != tc.unapplied*store.MicroCreditsPerCent {
				t.Fatalf("settle = %v, %+v, %v; want %d repaid and %d unapplied", created, warned, err, tc.repaid, tc.unapplied)
			}
			if bal, err := acme.CreditBalanceMicro(ctx); err != nil || bal != 0 {
				t.Fatalf("balance = %d, %v; want 0, with no prepaid credit from the warned card", bal, err)
			}
			if n := count(t, s, `SELECT COUNT(*) FROM card_refunds WHERE payment_intent = 'pi_recovery'`); n != 0 {
				t.Fatalf("queued refunds = %d, want none", n)
			}
			if n := count(t, s, `SELECT COUNT(*) FROM card_charges WHERE id = ? AND state = 'paid'`, rec.ChargeID); n != 1 {
				t.Fatalf("charge not paid; a later pay-now would charge the cleared debt again")
			}
			if _, err := acme.StartRecoveryAttempt(ctx, now); !errors.Is(err, store.ErrNoPayableCharge) {
				t.Fatalf("pay-now after settlement = %v, want ErrNoPayableCharge", err)
			}
			for range 2 {
				if _, err := s.AsOperator().TestOnlyReversePayment(ctx, "pi_recovery", "re_unapplied", "billing",
					tc.unapplied*store.MicroCreditsPerCent); err != nil {
					t.Fatalf("reverse the unapplied rest: %v", err)
				}
				if bal, err := acme.CreditBalanceMicro(ctx); err != nil || bal != 0 {
					t.Fatalf("balance = %d, %v; want returning the unapplied rest to leave the repayment", bal, err)
				}
			}
			if _, err := s.AsOperator().TestOnlyReversePayment(ctx, "pi_recovery", "re_rest", "billing",
				tc.repaid*store.MicroCreditsPerCent+1); err != nil {
				t.Fatalf("reverse the rest: %v", err)
			}
			if bal, err := acme.CreditBalanceMicro(ctx); err != nil || bal != -tc.repaid*store.MicroCreditsPerCent {
				t.Fatalf("balance = %d, %v; want a refund beyond the unapplied rest to reopen only the repaid debt", bal, err)
			}
		})
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
	if _, _, err := s.AsOperator().SettleCardPayment(ctx, store.CardPayment{
		ChargeID: "cardcharge-other", AttemptID: w.AttemptID, PaymentIntent: "pi_x", AmountCents: 15_000,
	}, now); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("an attempt settling another charge = %v, want ErrInvalidInput", err)
	}
	if _, _, err := s.AsOperator().SettleCardPayment(ctx, store.CardPayment{
		ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_short", AmountCents: 100,
	}, now); err != nil {
		t.Fatal(err)
	}
	if st, err := acme.SpendStanding(ctx, now); err != nil || st.OpenCharge == nil {
		t.Fatalf("standing = %+v, %v; want a short payment to leave the charge open", st, err)
	}
	if err := s.AsOperator().RecordAttemptIntent(ctx, w.AttemptID, "pi_full", w.Customer, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AsOperator().SettleCardPayment(ctx, store.CardPayment{
		ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_full", AmountCents: 15_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AsOperator().SettleCardPayment(ctx, store.CardPayment{
		ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_second", AmountCents: 15_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	due, err := s.AsOperator().DueCardRefunds(ctx, now)
	if err != nil || len(due) != 2 || due[0].PaymentIntent != "pi_short" || due[0].Reason != "amount_mismatch" ||
		due[1].PaymentIntent != "pi_second" || due[1].Reason != "duplicate" {
		t.Fatalf("queued refunds = %+v, %v; want the short and the second payment", due, err)
	}
	if err := s.AsOperator().MarkCardRefundMade(ctx, "acme", "pi_short-0", "re_1", "pending", now); err != nil {
		t.Fatal(err)
	}
	if due, err := s.AsOperator().DueCardRefunds(ctx, now); err != nil || len(due) != 1 {
		t.Fatalf("queued refunds after one was made = %+v, %v", due, err)
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
			note(acme.TestOnlySaveCard(ctx, store.Card{Customer: "cus_1", PaymentMethod: "pm_1", Fingerprint: "fp_1"}, "billing", now))
		},
		func(i int) {
			_, _, err := acme.SetBillingTrust(ctx, store.BillingTrustChange{
				Trust: store.BillingTrustGranted, Actor: "korey", Reason: "race",
			}, now)
			note(err)
		},
		func(i int) {
			work, _, err := s.AsOperator().DueCardCharges(ctx, now.Add(time.Duration(i)*time.Hour))
			note(err)
			for _, w := range work {
				_, _, err := s.AsOperator().SettleCardPayment(ctx, store.CardPayment{
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
			_, err = acme.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: store.MicroCreditsPerCredit, Reference: fmt.Sprintf("promo_%d", i), CreatedBy: "admin"})
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
	if err := s.AsOperator().RecordAttemptIntent(ctx, w.AttemptID, "pi_quiet", w.Customer, now); err != nil {
		t.Fatal(err)
	}
	if team, err := s.AsOperator().TestOnlyRecordPaymentWarning(ctx, store.PaymentWarning{
		WarningID: "issfr_quiet", PaymentIntent: "pi_quiet", Fingerprint: "fp_1",
	}, now); err != nil || team != "" {
		t.Fatalf("non-actionable warning = %q, %v; want recorded with no hold", team, err)
	}
	if created, _, err := s.AsOperator().SettleCardPayment(ctx, store.CardPayment{
		ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_quiet", AmountCents: 15_000, Fingerprint: "fp_1",
	}, now); err != nil || !created {
		t.Fatalf("settle = %v, %v; want the payment granted", created, err)
	}
	if freeze, err := acme.CreditFreeze(ctx); err != nil || freeze.Frozen {
		t.Fatalf("freeze = %+v, %v; want none", freeze, err)
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
	if _, _, err := s.AsOperator().FailCardAttempt(ctx, w.AttemptID, "pi_dead", "card_declined", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(ctx, store.PaymentWarning{
		WarningID: "issfr_saved", PaymentIntent: "pi_old", Fingerprint: "fp_1", Actionable: true,
	}, now); err != nil {
		t.Fatal(err)
	}
	rec, err := acme.StartRecoveryAttempt(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if created, _, err := s.AsOperator().SettleCardPayment(ctx, store.CardPayment{
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
	if created, _, err := s.AsOperator().SettleCardPayment(ctx, store.CardPayment{
		ChargeID: next.ChargeID, AttemptID: next.AttemptID, PaymentIntent: "pi_saved_card", AmountCents: 15_000,
		Fingerprint: "fp_1",
	}, now); err != nil || created {
		t.Fatalf("a payment by the warned card = %v, %v; want refused", created, err)
	}
}

// Only a refund made under the queue's own key moves it. An operator's partial
// refund while ours is due leaves ours to run, both are reversed, and the two
// never take back more than the payment; a report under another attempt's key
// leaves the retry state alone.
func TestOnlyTheQueuesOwnRefundMovesIt(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	acme := teamHandle(t, s, "acme")
	trustWithCard(t, acme, now)
	spend(t, s, "acme", "ch_1", 15_000)
	w := dueCharge(t, s, now)
	if _, _, err := s.AsOperator().SettleCardPayment(ctx, store.CardPayment{
		ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_dup", AmountCents: 1_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	paid := int64(1_000) * store.MicroCreditsPerCent
	if _, err := s.AsOperator().TestOnlyReversePayment(ctx, "pi_dup", "re_operator", "card", paid*4/10); err != nil {
		t.Fatal(err)
	}
	if moved, err := s.AsOperator().TestOnlyReportCardRefund(ctx, "", "re_operator", "succeeded", now); err != nil || moved {
		t.Fatalf("an operator's refund without our key = %v, %v; want the queue untouched", moved, err)
	}
	due, err := s.AsOperator().DueCardRefunds(ctx, now)
	if err != nil || len(due) != 1 || due[0].Key != "pi_dup-0" {
		t.Fatalf("due after an operator's partial refund = %+v, %v; want ours still due", due, err)
	}
	if err := s.AsOperator().MarkCardRefundMade(ctx, "acme", "pi_dup-0", "re_ours", "pending", now); err != nil {
		t.Fatal(err)
	}
	if moved, err := s.AsOperator().TestOnlyReportCardRefund(ctx, "pi_dup-0", "re_ours", "succeeded", now); err != nil || !moved {
		t.Fatalf("our refund's success = %v, %v", moved, err)
	}
	rev, err := s.AsOperator().TestOnlyReversePayment(ctx, "pi_dup", "re_ours", "card", paid)
	if err != nil || rev.ReversedMicro != paid {
		t.Fatalf("reversal of ours = %+v, %v; want the two reversals to total exactly the payment", rev, err)
	}

	spend(t, s, "acme", "ch_2", 15_000)
	w2 := dueCharge(t, s, now.Add(time.Hour))
	if _, _, err := s.AsOperator().SettleCardPayment(ctx, store.CardPayment{
		ChargeID: w2.ChargeID, AttemptID: w2.AttemptID, PaymentIntent: "pi_dup2", AmountCents: 1_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().MarkCardRefundMade(ctx, "acme", "pi_dup2-0", "re_a", "pending", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().TestOnlyReportCardRefund(ctx, "pi_dup2-0", "re_a", "failed", now); err != nil {
		t.Fatal(err)
	}
	for _, foreign := range []string{"", "pi_dup2-0", "pi_dup2-7"} {
		if moved, err := s.AsOperator().TestOnlyReportCardRefund(ctx, foreign, "re_foreign", "failed", now); err != nil || moved {
			t.Fatalf("a failure under key %q = %v, %v; want the retry state unchanged", foreign, moved, err)
		}
	}
	due, err = s.AsOperator().DueCardRefunds(ctx, now.Add(2*time.Hour))
	if err != nil || len(due) != 1 || due[0].Key != "pi_dup2-1" {
		t.Fatalf("due after foreign failures = %+v, %v; want only our one retry", due, err)
	}
}
