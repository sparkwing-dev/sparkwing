package store

import (
	"fmt"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func cardFixture(t *testing.T) (*Store, *Tenant, time.Time) {
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

func openRefill(t *testing.T, s *Store, team Team, cents int64, payment string) Refill {
	t.Helper()
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return setRefillTx(t.Context(), tx, team, RefillSettings{Enabled: true, ThresholdCents: 10_000_000, AmountCents: cents}, "owner")
	}); err != nil {
		t.Fatal(err)
	}
	due, err := s.AsOperator().DueRefills(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range due {
		if r.Team == team {
			if err := s.AsOperator().RecordRefillIntent(t.Context(), team, r.ID, payment); err != nil {
				t.Fatal(err)
			}
			r.PaymentIntent = payment
			return r
		}
	}
	t.Fatalf("no refill opened for %s: %+v", team, due)
	return Refill{}
}

func refillPaid(now time.Time, payment string, cents int64) capability.PaymentEvent {
	e := providerPaymentEvent(now, capability.PaymentCardPaid)
	e.EventID, e.PaymentID, e.ObjectID, e.AmountMinor = "paid-"+payment, payment, payment, cents
	e.CustomerID, e.MethodID, e.Status, e.Fingerprint = "cus_1", "pm_1", "succeeded", "fp_1"
	return e
}

func queuedRefundFixture(t *testing.T, paidFirst bool) (*Store, *Tenant, time.Time) {
	t.Helper()
	s, tenant, now := cardFixture(t)
	openRefill(t, s, "a", 1_000, "payment")
	warning := providerPaymentEvent(now, capability.PaymentWarningEvent)
	warning.AmountMinor, warning.Currency, warning.Fingerprint, warning.Actionable = 0, "", "fp_1", true
	events := []capability.PaymentEvent{warning, refillPaid(now, "payment", 1_000)}
	if paidFirst {
		events[0], events[1] = events[1], events[0]
	}
	for _, e := range events {
		if _, err := s.settlePaymentFacts(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	return s, tenant, now
}

func TestAFailedQueuedRefundIsRetried(t *testing.T) {
	s, _, now := queuedRefundFixture(t, false)
	ctx := t.Context()
	due, err := s.AsOperator().DueCardRefunds(ctx, now)
	if err != nil || len(due) != 1 || due[0].Key != "payment-0" {
		t.Fatalf("due = %+v, %v", due, err)
	}
	if err := s.AsOperator().MarkCardRefundMade(ctx, "a", "payment-0", "re_1", "pending", now); err != nil {
		t.Fatal(err)
	}
	if due, err := s.AsOperator().DueCardRefunds(ctx, now); err != nil || len(due) != 0 {
		t.Fatalf("a pending refund = %+v, %v; want not due", due, err)
	}
	event := providerPaymentEvent(now, capability.PaymentRefund)
	event.PaymentID, event.ObjectID, event.Status = "payment", "re_1", "failed"
	if _, err := s.settlePaymentFacts(ctx, event); err != nil {
		t.Fatal(err)
	}
	if due, err := s.AsOperator().DueCardRefunds(ctx, now.Add(30*time.Minute)); err != nil || len(due) != 0 {
		t.Fatalf("a failed refund inside its backoff = %+v, %v", due, err)
	}
	due, err = s.AsOperator().DueCardRefunds(ctx, now.Add(2*time.Hour))
	if err != nil || len(due) != 1 || due[0].Key != "payment-1" {
		t.Fatalf("a failed refund after its backoff = %+v, %v; want it due under a new key", due, err)
	}
	if err := s.AsOperator().MarkCardRefundMade(ctx, "a", "payment-1", "re_2", "succeeded", now); err != nil {
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
			s, _, now := queuedRefundFixture(t, false)
			ctx := t.Context()
			event := providerPaymentEvent(now, capability.PaymentRefund)
			event.PaymentID, event.ObjectID, event.Status = "payment", "re_1", status
			for _, id := range []string{"early", "early", "early-again"} {
				event.EventID = id
				if _, err := s.settlePaymentFacts(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := s.AsOperator().MarkCardRefundMade(ctx, "a", "payment-0", "re_1", test.response, now); err != nil {
					t.Fatal(err)
				}
			}
			var state string
			var attempts int
			if err := s.db.QueryRow("SELECT status,attempts FROM card_refunds WHERE team='a' AND payment_intent='payment'").Scan(&state, &attempts); err != nil {
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
			if wantState == CardRefundSucceeded && len(due) != 0 || wantState == CardRefundDue && (len(due) != 1 || due[0].Key != "payment-1") {
				t.Fatalf("refund after recorded %s: %+v", status, due)
			}
		})
	}
}

func TestRecordedRefundCannotBindAnotherAssociationProperty(t *testing.T) {
	property := func(seed uint32, outcome uint8) bool {
		s, _, now := queuedRefundFixture(t, false)
		ctx := t.Context()
		event := providerPaymentEvent(now, capability.PaymentRefund)
		foreign := fmt.Sprintf("other-' OR 1=1 --/雪/%x", seed)
		event.ObjectID, event.Status, event.AmountMinor = "bound-refund", []string{"failed", "canceled", "succeeded"}[outcome%3], 1_000
		if seed%4 == 0 {
			event.ObjectID = foreign
		}
		if _, err := s.settlePaymentFacts(ctx, event); err != nil {
			t.Fatal(err)
		}
		switch seed % 4 {
		case 1:
			rewriteSchemaExec(t, s.db, "UPDATE payment_refunds SET payment_intent=?", foreign)
		case 2:
			if err := s.AsOperator().SetPaymentMode(ctx, capability.PaymentLive); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.AsOperator().MarkCardRefundMade(ctx, "a", "payment-0", "bound-refund", "pending", now); err != nil {
			t.Fatal(err)
		}
		var status, id string
		var attempts int
		if err := s.db.QueryRow("SELECT status,refund_id,attempts FROM card_refunds WHERE team='a' AND payment_intent='payment'").Scan(&status, &id, &attempts); err != nil {
			t.Fatal(err)
		}
		switch {
		case event.Status == CardRefundSucceeded:
			return status == CardRefundSucceeded
		case seed%4 == 3:
			return status == CardRefundDue && id == "bound-refund" && attempts == 1
		}
		return status == CardRefundPending && id == "bound-refund" && attempts == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24, Rand: rand.New(rand.NewSource(42))}); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyTheQueuesOwnRefundMovesIt(t *testing.T) {
	s, tenant, now := queuedRefundFixture(t, true)
	ctx := t.Context()
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
	s, _, now := queuedRefundFixture(t, false)
	ctx := t.Context()
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
