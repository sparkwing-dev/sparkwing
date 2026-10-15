package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPaymentDeclineHoldsPrepaidClaimsUntilCollectionSucceeds(t *testing.T) {
	s, tenant, now := providerPaymentFixture(t)
	failed := providerCollectionEvent(t, s, now, capability.PaymentCardFailed)
	if _, err := s.settlePaymentFacts(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 400 * 100 * MicroCreditsPerCent, Reference: "promo", CreatedBy: "console"}); err != nil {
		t.Fatal(err)
	}
	if standing, err := tenant.BillingStanding(t.Context(), now); err != nil || standing.Trusted {
		t.Fatalf("declined prepaid standing=%+v: %v", standing, err)
	}
	_, demand := unitClaimFixtureStore(t, s)
	s.clock = func() time.Time { return now }
	rewriteSchemaExec(t, s.db, "UPDATE triggers SET expires_at=?", now.Add(48*time.Hour).UnixNano())
	root := authorityRoot("metered", "a")
	root.RootMetered = true
	authorityPersist(t, s, root)
	demand.CapabilityPrefix = root.Prefix
	_, err := s.ClaimUnit(t.Context(), demand)
	var refusal *InsufficientCreditsError
	if !errors.As(err, &refusal) || refusal.Limit != SpendLimitChargeFailed {
		t.Fatalf("declined prepaid claim=%v", err)
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 0)
	row, err := tenant.ReadNode(t.Context(), NodeRef{"run", "job"})
	if err != nil || row.Status != StatusPending || row.ClaimedBy != "" {
		t.Fatalf("refused claim changed work: %+v: %v", row, err)
	}
	now = now.Add(25 * time.Hour)
	retry := cardCollectionDue(t, s, now)
	if err := s.AsOperator().RecordAttemptIntent(t.Context(), retry.AttemptID, "retry-payment", retry.Customer, now); err != nil {
		t.Fatal(err)
	}
	paid := providerPaymentEvent(now, capability.PaymentCardPaid)
	paid.ObjectID, paid.PaymentID = "retry-payment", "retry-payment"
	paid.AmountMinor, paid.CustomerID, paid.MethodID = retry.AmountCents, retry.Customer, retry.PaymentMethod
	paid.Fingerprint, paid.Status = "fp_1", "succeeded"
	if _, err := s.settlePaymentFacts(t.Context(), paid); err != nil {
		t.Fatal(err)
	}
	if freeze, err := tenant.CreditFreeze(t.Context()); err != nil || freeze.Frozen {
		t.Fatalf("paid collection retained hold=%+v: %v", freeze, err)
	}
	if standing, err := tenant.BillingStanding(t.Context(), now); err != nil || !standing.Trusted {
		t.Fatalf("paid collection standing=%+v: %v", standing, err)
	}
	if claim, err := s.ClaimUnit(t.Context(), demand); err != nil || claim.RunID != "run" || claim.NodeID != "job" || len(claim.Bearer) == 0 {
		t.Fatalf("paid collection did not restore claims: %+v: %v", claim, err)
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 1)
	rewriteSchemaCount(t, s.db, "payment_events", 2)
}
