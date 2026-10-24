package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPaymentSettlementRefusesDeletingTeam(t *testing.T) {
	for _, kind := range []capability.PaymentEventKind{capability.PaymentCheckoutPaid, capability.PaymentWarningEvent, capability.PaymentRefund} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, now := providerPaymentFixture(t)
			if kind != capability.PaymentCheckoutPaid {
				providerCheckoutPaid(t, s)
			}
			rewriteSchemaExec(t, s.db, `UPDATE credit_checkouts SET expires_at=1`)
			if _, _, err := s.AsOperator().RequestTeamDeletion(t.Context(), "a"); err != nil {
				t.Fatal(err)
			}
			event := providerPaymentEvent(now, kind)
			event.Status, event.Actionable = "succeeded", true
			if kind == capability.PaymentCheckoutPaid {
				event.CheckoutID = "provider-session"
			}
			if _, err := s.settlePaymentFacts(t.Context(), event); !errors.Is(err, ErrTeamBeingDeleted) {
				t.Fatalf("settlement during deletion: %v", err)
			}
			rewriteSchemaCount(t, s.db, "card_refunds", 0)
		})
	}
}

func TestCardSetupSettlementRefusesDeletingTeam(t *testing.T) {
	for _, page := range []bool{false, true} {
		t.Run(map[bool]string{false: "setup", true: "page"}[page], func(t *testing.T) {
			s, _, now := providerPaymentFixture(t)
			kind := "setup"
			if page {
				kind = "setup_session"
			}
			rewriteSchemaExec(t, s.db, `INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id) VALUES ('test','setup',?,'a','new-customer')`, kind)
			rewriteSchemaExec(t, s.db, `UPDATE credit_checkouts SET expires_at=1`)
			if _, _, err := s.AsOperator().RequestTeamDeletion(t.Context(), "a"); err != nil {
				t.Fatal(err)
			}
			event := capability.PaymentEvent{EventID: "setup", ObjectID: "setup", CustomerID: "new-customer", Mode: capability.PaymentTest, Kind: capability.PaymentCardSetup, MethodID: "new-method", Fingerprint: "new-card", Created: now.Unix() + 1}
			if page {
				event.Kind, event.IntentID = capability.PaymentPageCompleted, "setup-intent"
			}
			if _, err := s.settlePaymentFacts(t.Context(), event); !errors.Is(err, ErrTeamBeingDeleted) {
				t.Fatalf("setup during deletion: %v", err)
			}
		})
	}
}

func TestTeamPurgeRollsBackEveryTableOnFailure(t *testing.T) {
	s, _, _ := providerPaymentFixture(t)
	providerCheckoutPaid(t, s)
	if _, _, err := s.AsOperator().RequestTeamDeletion(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AsOperator().ClaimTeamDeletion(t.Context(), "a", "purger", time.Minute); err != nil || !ok {
		t.Fatalf("claim=%v: %v", ok, err)
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER fail_checkout_purge BEFORE DELETE ON credit_checkouts BEGIN SELECT RAISE(ABORT,'cannot purge checkout'); END`)
	if err := s.AsOperator().PurgeTeam(t.Context(), "a", "purger", time.Hour); err == nil {
		t.Fatal("purge succeeded despite the failed delete")
	}
	for _, table := range []string{"payment_events", "credit_checkouts", "credit_grants"} {
		rewriteSchemaCount(t, s.db, table, 1)
	}
}

func TestPurgeRechecksOpenCheckoutsAndPayments(t *testing.T) {
	for _, pendingPayment := range []bool{false, true} {
		t.Run(map[bool]string{false: "checkout", true: "expired_payment"}[pendingPayment], func(t *testing.T) {
			s, _, now := providerPaymentFixture(t)
			rewriteSchemaExec(t, s.db, `UPDATE credit_checkouts SET expires_at=1`)
			if _, _, err := s.AsOperator().RequestTeamDeletion(t.Context(), "a"); err != nil {
				t.Fatal(err)
			}
			if ok, err := s.AsOperator().ClaimTeamDeletion(t.Context(), "a", "purger", time.Minute); err != nil || !ok {
				t.Fatalf("claim=%v: %v", ok, err)
			}
			if pendingPayment {
				rewriteSchemaExec(t, s.db, `UPDATE credit_checkouts SET payment_id='pending-payment'`)
			} else {
				rewriteSchemaExec(t, s.db, `UPDATE credit_checkouts SET expires_at=?`, now.Add(time.Hour).UnixNano())
			}
			if err := s.AsOperator().PurgeTeam(t.Context(), "a", "purger", time.Hour); !errors.Is(err, ErrOpenCheckout) {
				t.Fatalf("purge with money in flight: %v", err)
			}
			rewriteSchemaCount(t, s.db, "credit_checkouts", 1)
		})
	}
}
