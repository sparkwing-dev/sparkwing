package store

import (
	"errors"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func cardCollectionFixture(t *testing.T) (*Store, *Tenant, time.Time) {
	t.Helper()
	s, _ := paymentFixture(t, capability.PaymentTest)
	now := time.Date(2026, 10, 15, 12, 0, 0, 123, time.UTC)
	s.clock = func() time.Time { return now }
	tenant := &Tenant{s: s, team: "a"}
	if _, _, err := tenant.SetBillingTrust(t.Context(), BillingTrustChange{Trust: BillingTrustGranted, Actor: "console", Reason: "customer"}, now); err != nil {
		t.Fatal(err)
	}
	if err := tenant.TestOnlySaveCard(t.Context(), Card{Customer: "cus_1", PaymentMethod: "pm_1", Fingerprint: "fp_1", Brand: "visa", Last4: "4242"}, "billing", now); err != nil {
		t.Fatal(err)
	}
	return s, tenant, now
}

func cardCollectionDebt(t *testing.T, s *Store, id string, cents int64) {
	t.Helper()
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES (?,'a','','','','usage',0,?,?)`, id, cents*MicroCreditsPerCent, s.now().UnixNano())
}

func cardCollectionDue(t *testing.T, s *Store, now time.Time) CardChargeWork {
	t.Helper()
	work, _, err := s.AsOperator().DueCardCharges(t.Context(), now)
	if err != nil || len(work) != 1 {
		t.Fatalf("due charges=%+v: %v", work, err)
	}
	return work[0]
}

func TestADebtAtTheRungIsChargedOnce(t *testing.T) {
	s, acme, now := cardCollectionFixture(t)
	ctx := t.Context()

	cardCollectionDebt(t, s, "ch_small", 100)
	if work, _, err := s.AsOperator().DueCardCharges(ctx, now); err != nil || len(work) != 0 {
		t.Fatalf("a $1 debt under the $5 rung = %+v, %v; want no charge", work, err)
	}
	cardCollectionDebt(t, s, "ch_more", 400)

	work, _, err := s.AsOperator().DueCardCharges(ctx, now)
	if err != nil || len(work) != 1 || work[0].AmountCents != 500 || work[0].PaymentMethod != "pm_1" {
		t.Fatalf("work = %+v, %v; want one $5 charge to pm_1", work, err)
	}
	if again, _, err := s.AsOperator().DueCardCharges(ctx, now.Add(time.Second)); err != nil || len(again) != 0 {
		t.Fatalf("a second pass = %+v, %v; want the live attempt left alone", again, err)
	}
	if again, _, err := s.AsOperator().DueCardCharges(ctx, now.Add(3*time.Minute)); err != nil || len(again) != 1 ||
		again[0].AttemptID != work[0].AttemptID {
		t.Fatalf("a stalled attempt = %+v, %v; want it asked again under the same id", again, err)
	}

	pay := CardPayment{
		Team: "a", ChargeID: work[0].ChargeID, AttemptID: work[0].AttemptID,
		PaymentIntent: "pi_card_1", AmountCents: 500,
	}
	if created, _, err := s.AsOperator().SettleCardPayment(ctx, pay, now); err != nil || !created {
		t.Fatalf("settle = %v, %v", created, err)
	}
	if created, _, err := s.AsOperator().SettleCardPayment(ctx, pay, now); err != nil || created {
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
	if _, _, err := s.AsOperator().SettleCardPayment(ctx, other, now); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a payment naming another team = %v, want ErrInvalidInput", err)
	}
}

func TestConcurrentDebtChecksOpenOneCardCharge(t *testing.T) {
	s, _, now := cardCollectionFixture(t)
	ctx := t.Context()

	cardCollectionDebt(t, s, "ch_rung", 12_050)
	path := blobAccountingDiskStore(t, s)
	other := &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}

	start := make(chan struct{})
	type result struct {
		work []CardChargeWork
		err  error
	}
	done := make(chan result, 2)
	for _, client := range []*Store{s, other} {
		go func() {
			<-start
			work, _, err := client.AsOperator().DueCardCharges(ctx, now)
			done <- result{work: work, err: err}
		}()
	}
	close(start)
	var issued []CardChargeWork
	for range 2 {
		res := <-done
		if res.err != nil {
			t.Fatal(res.err)
		}
		issued = append(issued, res.work...)
	}
	if len(issued) != 1 || issued[0].AmountCents != 12_050 || issued[0].ChargeID == "" || issued[0].AttemptID == "" {
		t.Fatalf("concurrent due work = %+v, want one $120.50 attempt", issued)
	}
	var charges, attempts int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM card_charges WHERE team = 'a'`).Scan(&charges); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM card_attempts WHERE team = 'a'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if charges != 1 || attempts != 1 {
		t.Fatalf("persisted charges = %d, attempts = %d; want one each", charges, attempts)
	}
}

func TestADeclineHoldsTheTeamUntilPaid(t *testing.T) {
	s, acme, now := cardCollectionFixture(t)
	ctx := t.Context()

	cardCollectionDebt(t, s, "ch_1", 15_000)
	work, _, err := s.AsOperator().DueCardCharges(ctx, now)
	if err != nil || len(work) != 1 {
		t.Fatalf("work = %+v, %v", work, err)
	}
	if _, err := acme.StartRecoveryAttempt(ctx, now); !errors.Is(err, ErrNoPayableCharge) {
		t.Fatalf("pay now during a live attempt = %v, want ErrNoPayableCharge", err)
	}
	team, first, err := s.AsOperator().FailCardAttempt(ctx, work[0].AttemptID, "pi_declined", "insufficient_funds", now)
	if err != nil || team != "a" || !first {
		t.Fatalf("fail = %s %v %v", team, first, err)
	}
	st, err := acme.SpendStanding(ctx, now)
	if err != nil || !st.ChargeFailed() || st.Billing.Tier != "Held" {
		t.Fatalf("standing = %+v, %v; want the card's credit withdrawn", st, err)
	}
	if _, limit := st.Headroom(); limit != SpendLimitChargeFailed {
		t.Fatalf("binding limit = %q, want charge_failed", limit)
	}
	if again, _, err := s.AsOperator().DueCardCharges(ctx, now.Add(23*time.Hour)); err != nil || len(again) != 0 {
		t.Fatalf("a retry before a day = %+v, %v", again, err)
	}
	if again, _, err := s.AsOperator().DueCardCharges(ctx, now.Add(25*time.Hour)); err != nil || len(again) != 1 {
		t.Fatalf("the retry after a day = %+v, %v", again, err)
	} else if _, _, err := s.AsOperator().FailCardAttempt(ctx, again[0].AttemptID, "pi_2", "insufficient_funds", now); err != nil {
		t.Fatal(err)
	}

	rec, err := acme.StartRecoveryAttempt(ctx, now)
	if err != nil || rec.ChargeID != work[0].ChargeID || rec.AmountCents != 15_000 {
		t.Fatalf("recovery = %+v, %v", rec, err)
	}
	if _, _, err := s.AsOperator().SettleCardPayment(ctx, CardPayment{
		ChargeID: rec.ChargeID, AttemptID: rec.AttemptID,
		PaymentIntent: "pi_paid", AmountCents: 15_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	if st, err := acme.SpendStanding(ctx, now); err != nil || st.ChargeFailed() || !st.CardBilled() {
		t.Fatalf("after paying = %+v, %v; want the card's credit back", st, err)
	}
}

func TestAnAttemptPastTheKeyLifeIsReplaced(t *testing.T) {
	s, _, now := cardCollectionFixture(t)

	cardCollectionDebt(t, s, "ch_1", 15_000)
	first := cardCollectionDue(t, s, now)
	next := cardCollectionDue(t, s, now.Add(24*time.Hour))
	if next.AttemptID == first.AttemptID || next.PaymentIntent != "" {
		t.Fatalf("attempt after a day = %+v; want a new attempt, not the old key", next)
	}
	if err := s.AsOperator().RecordAttemptIntent(t.Context(), next.AttemptID, "pi_bound", next.Customer, now); err != nil {
		t.Fatal(err)
	}
	later := cardCollectionDue(t, s, now.Add(72*time.Hour))
	if later.AttemptID != next.AttemptID || later.PaymentIntent != "pi_bound" {
		t.Fatalf("attempt with a recorded payment = %+v; want it read by that payment", later)
	}
}

func TestAFailedQueuedRefundIsRetried(t *testing.T) {
	s, _, now := cardCollectionFixture(t)
	ctx := t.Context()

	cardCollectionDebt(t, s, "ch_1", 15_000)
	w := cardCollectionDue(t, s, now)
	if _, _, err := s.AsOperator().SettleCardPayment(ctx, CardPayment{
		ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_short", AmountCents: 100,
	}, now); err != nil {
		t.Fatal(err)
	}
	due, err := s.AsOperator().DueCardRefunds(ctx, now)
	if err != nil || len(due) != 1 || due[0].Key != "pi_short-0" {
		t.Fatalf("due = %+v, %v", due, err)
	}
	if err := s.AsOperator().MarkCardRefundMade(ctx, "a", "pi_short-0", "re_1", "pending", now); err != nil {
		t.Fatal(err)
	}
	if due, err := s.AsOperator().DueCardRefunds(ctx, now); err != nil || len(due) != 0 {
		t.Fatalf("a pending refund = %+v, %v; want not due", due, err)
	}
	if queued, err := s.AsOperator().TestOnlyReportCardRefund(ctx, "pi_short-0", "re_1", "failed", now); err != nil || !queued {
		t.Fatalf("report failed = %v, %v", queued, err)
	}
	if due, err := s.AsOperator().DueCardRefunds(ctx, now.Add(30*time.Minute)); err != nil || len(due) != 0 {
		t.Fatalf("a failed refund inside its backoff = %+v, %v", due, err)
	}
	due, err = s.AsOperator().DueCardRefunds(ctx, now.Add(2*time.Hour))
	if err != nil || len(due) != 1 || due[0].Key != "pi_short-1" {
		t.Fatalf("a failed refund after its backoff = %+v, %v; want it due under a new key", due, err)
	}
	if err := s.AsOperator().MarkCardRefundMade(ctx, "a", "pi_short-1", "re_2", "succeeded", now); err != nil {
		t.Fatal(err)
	}
	if queued, err := s.AsOperator().TestOnlyReportCardRefund(ctx, "pi_short-0", "re_1", "failed", now); err != nil || queued {
		t.Fatalf("a stale report of the first refund = %v, %v; want ignored", queued, err)
	}
	if due, err := s.AsOperator().DueCardRefunds(ctx, now.Add(48*time.Hour)); err != nil || len(due) != 0 {
		t.Fatalf("a succeeded refund = %+v, %v; want done", due, err)
	}
}

func TestARefundOutcomeBeforeItsBindIsKept(t *testing.T) {
	for _, tc := range []struct {
		status  string
		dueLate bool
	}{{"failed", true}, {"succeeded", false}} {
		t.Run(tc.status, func(t *testing.T) {
			s, _, now := cardCollectionFixture(t)
			ctx := t.Context()

			cardCollectionDebt(t, s, "ch_1", 15_000)
			w := cardCollectionDue(t, s, now)
			if _, _, err := s.AsOperator().SettleCardPayment(ctx, CardPayment{
				ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "pi_short", AmountCents: 100,
			}, now); err != nil {
				t.Fatal(err)
			}
			if queued, err := s.AsOperator().TestOnlyReportCardRefund(ctx, "pi_short-0", "re_1", tc.status, now); err != nil || !queued {
				t.Fatalf("early %s report = %v, %v; want applied", tc.status, queued, err)
			}
			if _, err := s.AsOperator().TestOnlyReportCardRefund(ctx, "pi_short-0", "re_1", tc.status, now); err != nil {
				t.Fatal(err)
			}
			if err := s.AsOperator().MarkCardRefundMade(ctx, "a", "pi_short-0", "re_1", "pending", now); err != nil {
				t.Fatal(err)
			}
			due, err := s.AsOperator().DueCardRefunds(ctx, now.Add(2*time.Hour))
			switch {
			case err != nil:
				t.Fatal(err)
			case tc.dueLate && (len(due) != 1 || due[0].Key != "pi_short-1"):
				t.Fatalf("after an early failure = %+v; want one retry under the next key", due)
			case !tc.dueLate && len(due) != 0:
				t.Fatalf("after an early success = %+v; want nothing due", due)
			}
		})
	}
}

func TestCardCollectionPaymentKeepsPersistedPayerProperty(t *testing.T) {
	property := func(forged string, selector uint8, cents uint16) bool {
		s, payer, now := cardCollectionFixture(t)
		cardCollectionDebt(t, s, "debt", 12050+int64(cents))
		work := cardCollectionDue(t, s, now)
		payment := CardPayment{ChargeID: work.ChargeID, AttemptID: work.AttemptID, PaymentIntent: "payment_" + forged, AmountCents: work.AmountCents}
		invalid := payment
		switch selector % 3 {
		case 0:
			invalid.Team = Team("foreign_" + forged)
		case 1:
			invalid.ChargeID += "_foreign_" + forged
		case 2:
			invalid.AttemptID += "_foreign_" + forged
		}
		if created, _, err := s.AsOperator().SettleCardPayment(t.Context(), invalid, now); err == nil || created {
			t.Logf("foreign payment settled: created=%t err=%v", created, err)
			return false
		}
		rewriteSchemaCount(t, s.db, "credit_grants", 0)
		if created, _, err := s.AsOperator().SettleCardPayment(t.Context(), payment, now); err != nil || !created {
			t.Fatalf("persisted payer settlement=%t: %v", created, err)
		}
		if selector%2 == 0 {
			path := blobAccountingDiskStore(t, s)
			if err := s.db.Close(); err != nil {
				t.Fatal(err)
			}
			s.db = blobAccountingDB(t, path)
		}
		if created, _, err := s.AsOperator().SettleCardPayment(t.Context(), payment, now); err != nil || created {
			t.Logf("replay settled twice: created=%t err=%v", created, err)
			return false
		}
		rewriteSchemaCount(t, s.db, "credit_grants", 1)
		for _, tenant := range []*Tenant{payer, {s: s, team: "b"}} {
			balance, err := tenant.CreditBalanceMicro(t.Context())
			if err != nil || balance != 0 {
				t.Logf("team=%q balance=%d: %v", tenant.team, balance, err)
				return false
			}
		}
		return true
	}
	for _, forged := range []string{"", "%'--", "a/b", "雪", "\x00"} {
		for selector := range uint8(3) {
			if !property(forged, selector, 1) {
				t.Fatalf("forged=%q selector=%d", forged, selector)
			}
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48, Rand: rand.New(rand.NewSource(77281))}); err != nil {
		t.Fatal(err)
	}
}
