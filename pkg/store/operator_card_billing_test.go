package store

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func cardCollectionFixture(t *testing.T) (*Store, *Tenant, time.Time) {
	t.Helper()
	s, _ := paymentFixture(t, capability.PaymentTest)
	now := time.Date(2026, 10, 15, 12, 0, 0, 123, time.UTC)
	s.clock = func() time.Time { return now }
	tenant := &Tenant{s: s, team: "a"}
	if _, _, err := tenant.TestOnlySetBillingTrust(t.Context(), BillingTrustChange{Trust: BillingTrustGranted, Actor: "console", Reason: "customer"}, now); err != nil {
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
	if _, err := acme.TestOnlyStartRecoveryAttempt(ctx, now); !errors.Is(err, ErrNoPayableCharge) {
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

	rec, err := acme.TestOnlyStartRecoveryAttempt(ctx, now)
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
	if err := s.AsOperator().RecordAttemptIntent(ctx, w.AttemptID, "pi_short", w.Customer, now); err != nil {
		t.Fatal(err)
	}
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
	event := providerPaymentEvent(now, capability.PaymentRefund)
	event.PaymentID, event.ObjectID, event.Status = "pi_short", "re_1", "failed"
	if _, err := s.settlePaymentFacts(ctx, event); err != nil {
		t.Fatal(err)
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
	if got, err := s.settlePaymentFacts(ctx, event); err != nil || !got.Replayed {
		t.Fatalf("stale refund replay: %+v, %v", got, err)
	}
	if due, err := s.AsOperator().DueCardRefunds(ctx, now.Add(48*time.Hour)); err != nil || len(due) != 0 {
		t.Fatalf("a succeeded refund = %+v, %v; want done", due, err)
	}
}

func TestARefundOutcomeBeforeItsBindIsKept(t *testing.T) {
	for _, test := range []struct{ recorded, response string }{
		{"failed", "pending"},
		{"canceled", "pending"},
		{"succeeded", "pending"},
		{"succeeded", "failed"},
		{"failed", "succeeded"},
	} {
		status := test.recorded
		t.Run(status+"/"+test.response, func(t *testing.T) {
			s, _, now := cardCollectionFixture(t)
			ctx := t.Context()
			cardCollectionDebt(t, s, "ch_1", 15_000)
			work := cardCollectionDue(t, s, now)
			if err := s.AsOperator().RecordAttemptIntent(ctx, work.AttemptID, "pi_short", work.Customer, now); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.AsOperator().SettleCardPayment(ctx, CardPayment{ChargeID: work.ChargeID, AttemptID: work.AttemptID, PaymentIntent: "pi_short", AmountCents: 100}, now); err != nil {
				t.Fatal(err)
			}
			event := providerPaymentEvent(now, capability.PaymentRefund)
			event.PaymentID, event.ObjectID, event.Status = "pi_short", "re_1", status
			for _, id := range []string{"early", "early", "early-again"} {
				event.EventID = id
				if _, err := s.settlePaymentFacts(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := s.AsOperator().MarkCardRefundMade(ctx, "a", "pi_short-0", "re_1", test.response, now); err != nil {
					t.Fatal(err)
				}
			}
			var state string
			var attempts int
			if err := s.db.QueryRow("SELECT status,attempts FROM card_refunds WHERE team='a' AND payment_intent='pi_short'").Scan(&state, &attempts); err != nil {
				t.Fatal(err)
			}
			wantState, wantAttempts := CardRefundDue, 1
			if status == "succeeded" || test.response == "succeeded" {
				wantState, wantAttempts = CardRefundSucceeded, 0
			}
			if state != wantState || attempts != wantAttempts {
				t.Fatalf("bound refund lost its recorded outcome: status=%s attempts=%d, want %s/%d", state, attempts, wantState, wantAttempts)
			}
			due, err := s.AsOperator().DueCardRefunds(ctx, now.Add(2*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if wantState == CardRefundSucceeded && len(due) != 0 || wantState == CardRefundDue && (len(due) != 1 || due[0].Key != "pi_short-1") {
				t.Fatalf("refund after recorded %s: %+v", status, due)
			}
		})
	}
}

func TestRecordedRefundCannotBindAnotherAssociationProperty(t *testing.T) {
	property := func(seed uint32, outcome uint8) bool {
		s, _, now := cardCollectionFixture(t)
		ctx := t.Context()
		cardCollectionDebt(t, s, "debt", 15_000)
		work := cardCollectionDue(t, s, now)
		if err := s.AsOperator().RecordAttemptIntent(ctx, work.AttemptID, "payment", work.Customer, now); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.AsOperator().SettleCardPayment(ctx, CardPayment{ChargeID: work.ChargeID, AttemptID: work.AttemptID, PaymentIntent: "payment", AmountCents: 100}, now); err != nil {
			t.Fatal(err)
		}
		event := providerPaymentEvent(now, capability.PaymentRefund)
		foreign := fmt.Sprintf("other-' OR 1=1 --/雪/%x", seed)
		event.ObjectID, event.Status = "bound-refund", []string{"failed", "canceled", "succeeded"}[outcome%3]
		if seed%4 == 0 {
			event.ObjectID = foreign
		}
		if _, err := s.settlePaymentFacts(ctx, event); err != nil {
			t.Fatal(err)
		}
		switch seed % 4 {
		case 1:
			rewriteSchemaExec(t, s.db, "UPDATE payment_events SET team='b' WHERE event_kind='refund'")
		case 2:
			if err := s.AsOperator().SetPaymentMode(ctx, capability.PaymentLive); err != nil {
				t.Fatal(err)
			}
		case 3:
			rewriteSchemaExec(t, s.db, "UPDATE payment_events SET payment_id=? WHERE event_kind='refund'", foreign)
		}
		if err := s.AsOperator().MarkCardRefundMade(ctx, "a", "payment-0", "bound-refund", "pending", now); err != nil {
			t.Fatal(err)
		}
		var status, id string
		var attempts int
		if err := s.db.QueryRow("SELECT status,refund_id,attempts FROM card_refunds WHERE team='a' AND payment_intent='payment'").Scan(&status, &id, &attempts); err != nil {
			t.Fatal(err)
		}
		return status == CardRefundPending && id == "bound-refund" && attempts == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24, Rand: rand.New(rand.NewSource(42))}); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyTheQueuesOwnRefundMovesIt(t *testing.T) {
	s, tenant, now := cardCollectionFixture(t)
	ctx := t.Context()
	cardCollectionDebt(t, s, "debt", 15_000)
	work := cardCollectionDue(t, s, now)
	if err := s.AsOperator().RecordAttemptIntent(ctx, work.AttemptID, "payment", work.Customer, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AsOperator().SettleCardPayment(ctx, CardPayment{ChargeID: work.ChargeID, AttemptID: work.AttemptID, PaymentIntent: "payment", AmountCents: 1_000}, now); err != nil {
		t.Fatal(err)
	}
	owner := authorityMember(t, s, "operator", RoleReader)
	console := s.AsConsole(func(id string) bool { return id == "operator" })
	a := capability.Authorized{Principal: capability.Principal{Class: capability.CredentialConsole, Authority: owner.Authority, Capability: capability.Capability{Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "operator"}}}, Target: capability.Target{Team: "a"}}
	if _, err := console.ConsoleRefund(ctx, a, controllerwire.OperatorRefund{PaymentID: "payment", RefundID: "operator-refund", AmountCents: 400, Reason: "partial"}, "once"); err != nil {
		t.Fatal(err)
	}
	event := providerPaymentEvent(now, capability.PaymentRefund)
	event.ObjectID, event.Status, event.AmountMinor = "operator-refund", "succeeded", 400
	if _, err := s.settlePaymentFacts(ctx, event); err != nil {
		t.Fatal(err)
	}
	due, err := s.AsOperator().DueCardRefunds(ctx, now)
	if err != nil || len(due) != 1 || due[0].Key != "payment-0" {
		t.Fatalf("operator refund moved the queue: %+v, %v", due, err)
	}
	if err := s.AsOperator().MarkCardRefundMade(ctx, "a", "payment-0", "queued-refund", "pending", now); err != nil {
		t.Fatal(err)
	}
	event.EventID, event.ObjectID, event.AmountMinor = "queue-result", "queued-refund", 1_000
	for range 2 {
		if _, err := s.settlePaymentFacts(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if due, err := s.AsOperator().DueCardRefunds(ctx, now.Add(48*time.Hour)); err != nil || len(due) != 0 {
		t.Fatalf("successful refund remains queued: %+v, %v", due, err)
	}
	state, err := tenant.CreditState(ctx, 0)
	if err != nil || state.ReversedMicro != 1_000*MicroCreditsPerCent {
		t.Fatalf("operator and queued refunds exceeded the payment: %+v, %v", state, err)
	}
}

func TestRecordedRefundBindingRollsBackWithItsOutcome(t *testing.T) {
	s, _, now := cardCollectionFixture(t)
	ctx := t.Context()
	cardCollectionDebt(t, s, "debt", 15_000)
	work := cardCollectionDue(t, s, now)
	if err := s.AsOperator().RecordAttemptIntent(ctx, work.AttemptID, "payment", work.Customer, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AsOperator().SettleCardPayment(ctx, CardPayment{ChargeID: work.ChargeID, AttemptID: work.AttemptID, PaymentIntent: "payment", AmountCents: 100}, now); err != nil {
		t.Fatal(err)
	}
	event := providerPaymentEvent(now, capability.PaymentRefund)
	event.ObjectID, event.Status = "refund", "failed"
	if _, err := s.settlePaymentFacts(ctx, event); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_refund_outcome BEFORE UPDATE ON card_refunds WHEN NEW.attempts>OLD.attempts BEGIN SELECT RAISE(ABORT,'outcome unavailable'); END`)
	if err := s.AsOperator().MarkCardRefundMade(ctx, "a", "payment-0", "refund", "pending", now); err == nil {
		t.Fatal("refund binding committed without its recorded outcome")
	}
	var status, id string
	var attempts int
	if err := s.db.QueryRow("SELECT status,refund_id,attempts FROM card_refunds WHERE team='a' AND payment_intent='payment'").Scan(&status, &id, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != CardRefundDue || id != "" || attempts != 0 {
		t.Fatalf("partial refund binding survived rollback: %s/%s/%d", status, id, attempts)
	}
	rewriteSchemaExec(t, s.db, "DROP TRIGGER refuse_refund_outcome")
	if err := s.AsOperator().MarkCardRefundMade(ctx, "a", "payment-0", "refund", "pending", now); err != nil {
		t.Fatal(err)
	}
	due, err := s.AsOperator().DueCardRefunds(ctx, now.Add(2*time.Hour))
	if err != nil || len(due) != 1 || due[0].Key != "payment-1" {
		t.Fatalf("refund outcome was lost on retry: %+v, %v", due, err)
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
