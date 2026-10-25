package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestDeletionRetainsFailedRefundAfterCheckoutExpiry(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO payment_cards(fingerprint,first_team,payment_intent,pinned_at) VALUES ('pinned','b','original',1)`)
	event := capability.PaymentEvent{EventID: "refused", CheckoutID: "provider-session", PaymentID: "payment", Currency: "usd", AmountMinor: 100, Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid, Fingerprint: "pinned"}
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().MarkCardRefundMade(t.Context(), "a", "payment-0", "refund"); err != nil {
		t.Fatal(err)
	}
	refund := providerPaymentEvent(now, capability.PaymentRefund)
	refund.ObjectID, refund.Status = "refund", "failed"
	if _, err := s.settlePaymentFacts(t.Context(), refund); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE credit_checkouts SET expires_at=1`)
	purgePaymentTeam(t, s, "a")
	rewriteSchemaCount(t, s.db, "card_refunds", 1)
	s.SetTestClock(t, func() time.Time { return now.Add(2 * time.Hour) })
	due, err := s.AsOperator().DueCardRefunds(t.Context())
	if err != nil || len(due) != 1 || due[0].Team != "a" || due[0].PaymentIntent != "payment" {
		t.Fatalf("refund retry after purge=%+v: %v", due, err)
	}
}

func TestRevokedStandingReportsCreditFreeze(t *testing.T) {
	s := authorityStore(t)
	rewriteSchemaExec(t, s.db, `UPDATE teams SET billing_trust='revoked' WHERE name='a'`)
	tenant := &Tenant{s: s, team: "a"}
	freeze, err := tenant.CreditFreeze(t.Context())
	if err != nil || !freeze.Frozen {
		t.Fatalf("revoked freeze=%+v: %v", freeze, err)
	}
	standing, err := tenant.BillingStanding(t.Context())
	if err != nil || standing.HoldCause != holdCauseRevoked {
		t.Fatalf("standing=%+v: %v", standing, err)
	}
}

func TestOperatorGrantRequiresKeyAndAllowsFreeCorrection(t *testing.T) {
	s := authorityStore(t)
	tenant := &Tenant{s: s, team: "a"}
	req := CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 100, CreatedBy: "operator"}
	if _, err := tenant.RecordCreditGrant(t.Context(), req); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("unkeyed grant: %v", err)
	}
	req.Reference = "mistake"
	if _, err := tenant.RecordCreditGrant(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	correction := CreditGrantRequest{Kind: creditGrantReversal, AmountMicro: -100, Reference: "correction", Reverses: "mistake", CreatedBy: "operator"}
	for replay := range 2 {
		result, err := tenant.RecordCreditGrant(t.Context(), correction)
		if err != nil || result.Created != (replay == 0) {
			t.Fatalf("correction=%+v: %v", result, err)
		}
	}
	balance, err := tenant.CreditBalanceMicro(t.Context())
	if err != nil || balance != 0 {
		t.Fatalf("corrected balance=%d: %v", balance, err)
	}
	correction.Reference = "over-correction"
	if _, err := tenant.RecordCreditGrant(t.Context(), correction); err == nil {
		t.Fatal("reversed more than the free grant")
	}
}

func TestReversalMustReferenceExistingGrant(t *testing.T) {
	s := authorityStore(t)
	rewriteSchemaRefuses(t, s.db, `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,reverses,created_at) VALUES ('a','bad','reversal',-1,'refund','missing',1)`)
}

func TestBillingModelJobsHaveTheirOwnSpecIdentity(t *testing.T) {
	s, _, _ := billingModelFixture(t)
	billingModelJob(t, s, "second")
	var raw []byte
	if err := s.db.QueryRow(`SELECT spec_json FROM nodes WHERE team='a' AND node_id='second'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var spec struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil || spec.ID != "second" {
		t.Fatalf("second node spec=%s: %v", raw, err)
	}
}

func TestPurgeRetainsRefundQueuedAfterDeletionRequest(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE credit_checkouts SET expires_at=1`)
	if _, _, err := s.AsOperator().RequestTeamDeletion(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO card_refunds(payment_mode,team,payment_intent,reason,created_at) VALUES ('test','a','late','refused',?)`, now.UnixNano())
	if ok, err := s.AsOperator().ClaimTeamDeletion(t.Context(), "a", "purger", time.Minute); err != nil || !ok {
		t.Fatalf("claim=%v: %v", ok, err)
	}
	if err := s.AsOperator().PurgeTeam(t.Context(), "a", "purger", time.Hour); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "card_refunds", 1)
}
