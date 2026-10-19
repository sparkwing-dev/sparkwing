package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPaymentRefundFollowsCheckoutGrantAndReplaysAfterRestart(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	now := time.Unix(1000, 0).UTC()
	s.clock = func() time.Time { return now }
	path := blobAccountingDiskStore(t, s)
	paid, err := s.settlePaymentFacts(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	if team, err := s.AsOperator().TestOnlyPaymentTeam(t.Context(), event.PaymentID); err != nil || team != paid.Team {
		t.Fatalf("payment team=%q: %v", team, err)
	}
	first, err := s.AsOperator().TestOnlyReversePayment(t.Context(), event.PaymentID, "partial", "console", 300000)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.Team != paid.Team || first.PaidMicro != 1000000 || first.ReversedMicro != 300000 || first.BalanceMicro != 700000 || first.Grant == nil || first.Grant.AmountMicro != -300000 || first.Grant.Reverses != "payment:test:checkout" || !first.Grant.CreatedAt.Equal(now) {
		t.Fatalf("partial refund=%+v, grant=%+v", first, first.Grant)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = blobAccountingDB(t, path)
	again, err := s.AsOperator().TestOnlyReversePayment(t.Context(), event.PaymentID, "partial", "console", 300000)
	if err != nil || again.Created || again.Grant == nil || again.Grant.ID != first.Grant.ID || again.BalanceMicro != 700000 {
		t.Fatalf("refund replay=%+v: %v", again, err)
	}
	rest, err := s.AsOperator().TestOnlyReversePayment(t.Context(), event.PaymentID, "rest", "console", 2000000)
	if err != nil || !rest.Created || rest.Grant == nil || rest.Grant.AmountMicro != -700000 || rest.ReversedMicro != 1000000 || rest.BalanceMicro != 0 {
		t.Fatalf("remaining refund=%+v: %v", rest, err)
	}
	finished, err := s.AsOperator().TestOnlyReversePayment(t.Context(), event.PaymentID, "third", "console", 0)
	if err != nil || finished.Created || finished.Grant != nil || finished.ReversedMicro != 1000000 {
		t.Fatalf("exhausted payment=%+v: %v", finished, err)
	}
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	var balance int64
	if balance, err = (&Tenant{s: s, team: paid.Team}).CreditBalanceMicro(t.Context()); err != nil || balance != 0 {
		t.Fatalf("paid-event replay restored refunded credit=%d: %v", balance, err)
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 3)
}

func TestPaymentRefundSelectsConfiguredMode(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES ('live-checkout','b','provider-session','live','usd',100,1000000,1,2)`)
	rewriteSchemaExec(t, s.db, `UPDATE settings SET value=? WHERE key='payment_mode'`, []byte(`"live"`))
	event.Mode = capability.PaymentLive
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []capability.PaymentMode{capability.PaymentTest, capability.PaymentLive} {
		rewriteSchemaExec(t, s.db, `UPDATE settings SET value=? WHERE key='payment_mode'`, []byte(`"`+string(mode)+`"`))
		want := Team("a")
		if mode == capability.PaymentLive {
			want = "b"
		}
		if team, err := s.AsOperator().TestOnlyPaymentTeam(t.Context(), event.PaymentID); err != nil || team != want {
			t.Fatalf("%s payment team=%q, want %q: %v", mode, team, want, err)
		}
		got, err := s.AsOperator().TestOnlyReversePayment(t.Context(), event.PaymentID, "refund", "console", 0)
		if err != nil || got.Team != want || got.BalanceMicro != 0 || !got.Created {
			t.Fatalf("%s refund=%+v: %v", mode, got, err)
		}
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 4)
}

func TestPaymentRefundRequiresProviderPaymentAndValidGrantAssociation(t *testing.T) {
	for _, problem := range []string{"unpaid", "empty", "session", "checkout", "grant_reference", "other_mode", "missing_mode", "foreign_grant", "wrong_amount", "ambiguous"} {
		t.Run(problem, func(t *testing.T) {
			s, event := paymentFixture(t, capability.PaymentTest)
			payment := event.PaymentID
			if problem == "unpaid" {
				rewriteSchemaExec(t, s.db, "UPDATE credit_checkouts SET payment_id=?", payment)
			} else {
				if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			switch problem {
			case "empty":
				payment = ""
			case "session":
				payment = event.CheckoutID
			case "checkout":
				payment = "checkout"
			case "grant_reference":
				payment = "payment:test:checkout"
			case "other_mode":
				rewriteSchemaExec(t, s.db, `UPDATE settings SET value=? WHERE key='payment_mode'`, []byte(`"live"`))
			case "missing_mode":
				rewriteSchemaExec(t, s.db, "DELETE FROM settings WHERE key='payment_mode'")
			case "foreign_grant":
				rewriteSchemaExec(t, s.db, "UPDATE credit_grants SET team='b' WHERE kind='paid'")
			case "wrong_amount":
				rewriteSchemaExec(t, s.db, "UPDATE credit_grants SET amount_micro=1 WHERE kind='paid'")
			case "ambiguous":
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(id,team,kind,amount_micro,reference,created_by,created_at) VALUES ('card','b','paid',1000000,?,'card',1)`, payment)
			}
			if _, err := s.AsOperator().TestOnlyReversePayment(t.Context(), payment, "refund", "console", 0); err == nil {
				t.Fatal("invalid payment association refunded")
			}
			var reversals int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM credit_grants WHERE kind='reversal'").Scan(&reversals); err != nil || reversals != 0 {
				t.Fatalf("refused payment wrote %d reversals: %v", reversals, err)
			}
		})
	}
}

func TestPaymentRefundAuditFailureRollsBackAndPriorReversalsCount(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().TestOnlyReversePayment(t.Context(), event.PaymentID, "console-partial", "console", 300000); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_refund_audit BEFORE INSERT ON business_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if _, err := s.AsOperator().TestOnlyReversePayment(t.Context(), event.PaymentID, "refund", "console", 0); err == nil {
		t.Fatal("refund committed without its audit")
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 2)
	rewriteSchemaExec(t, s.db, "DROP TRIGGER refuse_refund_audit")
	got, err := s.AsOperator().TestOnlyReversePayment(t.Context(), event.PaymentID, "refund", "console", 0)
	if err != nil || got.Grant == nil || got.Grant.AmountMicro != -700000 || got.ReversedMicro != 1000000 || got.BalanceMicro != 0 {
		t.Fatalf("refund after partial reversal=%+v: %v", got, err)
	}
	if _, err := s.AsOperator().TestOnlyReversePayment(t.Context(), "unknown", "refund", "console", 0); !errors.Is(err, ErrUnknownPayment) {
		t.Fatalf("unknown payment=%v", err)
	}
}

func TestPaymentWarningFindsCheckoutTeam(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	warning := PaymentWarning{WarningID: "warning", PaymentIntent: event.PaymentID, Actionable: true}
	for range 2 {
		team, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), warning, s.now())
		if err != nil || team != "a" {
			t.Fatalf("warning team=%q: %v", team, err)
		}
	}
	rewriteSchemaCount(t, s.db, "credit_freezes", 1)
	var team, payment string
	if err := s.db.QueryRow("SELECT team,payment_id FROM credit_freezes").Scan(&team, &payment); err != nil || team != "a" || payment != event.PaymentID {
		t.Fatalf("warning hold=%q,%q: %v", team, payment, err)
	}
}

func TestAWarnedPurchaseIsNotGranted(t *testing.T) {
	for _, actionable := range []bool{false, true} {
		t.Run(map[bool]string{false: "informational", true: "actionable"}[actionable], func(t *testing.T) {
			s, event := paymentFixture(t, capability.PaymentTest)
			if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), PaymentWarning{WarningID: "warning", PaymentIntent: event.PaymentID, Actionable: actionable}, s.now()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
				t.Fatal(err)
			}
			if !actionable {
				rewriteSchemaCount(t, s.db, "card_refunds", 0)
				return
			}
			rewriteSchemaCount(t, s.db, "credit_grants", 0)
			var refunded, reason string
			if err := s.db.QueryRowContext(t.Context(), "SELECT payment_intent,reason FROM card_refunds WHERE team='a'").Scan(&refunded, &reason); err != nil || refunded != event.PaymentID || reason != CardRefusedWarning {
				t.Fatalf("warned checkout refund=%q,%q: %v", refunded, reason, err)
			}
			var closed int
			if err := s.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM credit_checkouts WHERE payment_id=? AND paid_at IS NULL AND grant_id IS NULL AND closed_at IS NOT NULL", event.PaymentID).Scan(&closed); err != nil || closed != 1 {
				t.Fatalf("warned checkout left open: %d, %v", closed, err)
			}
			rewriteSchemaCount(t, s.db, "credit_freezes", 1)
		})
	}
}

func TestPaymentRefundIndependentClientsCannotOverReverse(t *testing.T) {
	for _, sameReference := range []bool{false, true} {
		t.Run(map[bool]string{false: "distinct_references", true: "same_reference"}[sameReference], func(t *testing.T) {
			s, event := paymentFixture(t, capability.PaymentTest)
			path := blobAccountingDiskStore(t, s)
			if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
				t.Fatal(err)
			}
			other := &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}
			start := make(chan struct{})
			type result struct {
				reversal CreditReversal
				err      error
			}
			results := make(chan result, 2)
			for i, client := range []*Store{s, other} {
				reference := "refund"
				if i == 1 && !sameReference {
					reference = "other-refund"
				}
				go func() {
					<-start
					got, err := client.AsOperator().TestOnlyReversePayment(t.Context(), event.PaymentID, reference, "console", 0)
					results <- result{got, err}
				}()
			}
			close(start)
			created := 0
			for range 2 {
				got := <-results
				if got.err != nil {
					t.Error(got.err)
				}
				if got.reversal.Created {
					created++
				}
			}
			if created != 1 {
				t.Fatalf("refund winners=%d, want one", created)
			}
			rewriteSchemaCount(t, s.db, "credit_grants", 2)
			balance, err := (&Tenant{s: s, team: "a"}).CreditBalanceMicro(t.Context())
			if err != nil || balance != 0 {
				t.Fatalf("over-reversed balance=%d: %v", balance, err)
			}
		})
	}
}
