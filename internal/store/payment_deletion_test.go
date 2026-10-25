package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func purgePaymentTeam(t *testing.T, s *Store, team Team) {
	t.Helper()
	if _, _, err := s.AsOperator().RequestTeamDeletion(t.Context(), team); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AsOperator().ClaimTeamDeletion(t.Context(), team, "purger", time.Minute); err != nil || !ok {
		t.Fatalf("claim=%v: %v", ok, err)
	}
	if err := s.AsOperator().PurgeTeam(t.Context(), team, "purger", 0); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AsOperator().ClaimTeamDeletion(t.Context(), team, "recheck", time.Minute); err != nil || !ok {
		t.Fatalf("recheck claim=%v: %v", ok, err)
	}
	if err := s.AsOperator().FinishTeamRecheck(t.Context(), team, "recheck"); err != nil {
		t.Fatal(err)
	}
	var sensitive int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM teams WHERE name=? AND (display_name<>'' OR created_by<>'' OR card_customer<>'' OR card_payment_method<>'' OR card_fingerprint<>'' OR card_brand<>'' OR card_last4<>'' OR refill_enabled<>0)`, team).Scan(&sensitive); err != nil || sensitive != 0 {
		t.Fatalf("tombstone kept personal data or saved card: %d: %v", sensitive, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM credit_checkouts WHERE team=? AND receipt_email<>''`, team).Scan(&sensitive); err != nil || sensitive != 0 {
		t.Fatalf("checkout retained receipt address: %d: %v", sensitive, err)
	}
	if _, err := s.ForTeam(t.Context(), team); !errors.Is(err, ErrUnknownTeam) {
		t.Fatalf("tombstone reopened: %v", err)
	}
	if err := s.AsOperator().EachTeam(t.Context(), nil, func(tenant *Tenant) error {
		if tenant.Team() == team {
			t.Fatal("maintenance revisited deleted team")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderPendingRefundSurvivesTeamPurge(t *testing.T) {
	s, tenant, now := providerPaymentFixture(t)
	providerCheckoutPaid(t, s)
	refund := providerPaymentEvent(now, capability.PaymentRefund)
	refund.Status = cardRefundPending
	if _, err := s.settlePaymentFacts(t.Context(), refund); err != nil {
		t.Fatal(err)
	}
	purgePaymentTeam(t, s, "a")
	refund.EventID, refund.Status = "refund-success", cardRefundSucceeded
	for range 2 {
		if _, err := s.settlePaymentFacts(t.Context(), refund); err != nil {
			t.Fatal(err)
		}
	}
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 0 {
		t.Fatalf("refunded balance=%d: %v", balance, err)
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts", 1)
	rewriteSchemaCount(t, s.db, "credit_grants", 2)
	var status string
	if err := s.db.QueryRow(`SELECT status FROM payment_refunds WHERE refund_id=?`, refund.ObjectID).Scan(&status); err != nil || status != cardRefundSucceeded {
		t.Fatalf("refund status=%s: %v", status, err)
	}
}

func TestAuthenticationCheckoutSettlesAfterTeamPurge(t *testing.T) {
	s, tenant, _ := providerPaymentFixture(t)
	// The checkout is bound to an intent waiting for the customer's authentication.
	rewriteSchemaExec(t, s.db, `UPDATE credit_checkouts SET payment_id='payment',receipt_email='payer@example.test'`)
	purgePaymentTeam(t, s, "a")
	providerCheckoutPaid(t, s)
	providerCheckoutPaid(t, s)
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 100*MicroCreditsPerCent {
		t.Fatalf("late paid balance=%d: %v", balance, err)
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 1)
}

func TestWarningSharedWithDeletedTeamKeepsPayerConsequences(t *testing.T) {
	for _, purged := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "purged"}[purged], func(t *testing.T) {
			s, tenant, now := providerPaymentFixture(t)
			paid := capability.PaymentEvent{EventID: "paid", CheckoutID: "provider-session", PaymentID: "payment", Currency: "usd", AmountMinor: 100, Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid, Fingerprint: "shared"}
			if _, err := s.settlePaymentFacts(t.Context(), paid); err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, `UPDATE teams SET display_name='Personal name',created_by='owner',card_customer='customer',card_payment_method='method',card_fingerprint='shared',card_brand='visa',card_last4='4242',refill_enabled=1,refill_amount_cents=1000 WHERE name='b'`)
			if purged {
				purgePaymentTeam(t, s, "b")
			} else if _, _, err := s.AsOperator().RequestTeamDeletion(t.Context(), "b"); err != nil {
				t.Fatal(err)
			}
			warning := providerPaymentEvent(now, capability.PaymentWarningEvent)
			warning.Fingerprint, warning.Actionable = "shared", true
			if _, err := s.settlePaymentFacts(t.Context(), warning); err != nil {
				t.Fatal(err)
			}
			if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 0 {
				t.Fatalf("warning balance=%d: %v", balance, err)
			}
			refunds, err := s.AsOperator().DueCardRefunds(t.Context())
			if err != nil || len(refunds) != 1 || refunds[0].Team != "a" || refunds[0].PaymentIntent != "payment" {
				t.Fatalf("payer refund=%+v: %v", refunds, err)
			}
			hold, err := tenant.CreditFreeze(t.Context())
			if err != nil || !hold.Frozen {
				t.Fatalf("payer hold=%+v: %v", hold, err)
			}
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
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER fail_team_purge BEFORE UPDATE ON teams BEGIN SELECT RAISE(ABORT,'cannot purge team'); END`)
	if err := s.AsOperator().PurgeTeam(t.Context(), "a", "purger", time.Hour); err == nil {
		t.Fatal("purge succeeded despite the failed delete")
	}
	for _, table := range []string{"payment_events", "credit_checkouts", "credit_grants"} {
		rewriteSchemaCount(t, s.db, table, 1)
	}
}

func TestPurgeKeepsOpenCheckoutsAndPayments(t *testing.T) {
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
			if err := s.AsOperator().PurgeTeam(t.Context(), "a", "purger", time.Hour); err != nil {
				t.Fatalf("purge with money in flight: %v", err)
			}
			rewriteSchemaCount(t, s.db, "credit_checkouts", 1)
		})
	}
}
