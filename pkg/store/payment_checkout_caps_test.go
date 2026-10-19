package store

import (
	"errors"
	"testing"
	"time"
)

func checkoutCapFixture(t *testing.T, trusted bool) (*Store, *Tenant, *Tenant, *time.Time) {
	t.Helper()
	s := authorityStore(t)
	now := time.Unix(1000, 0).UTC()
	s.clock = func() time.Time { return now }
	rewriteSchemaExec(t, s.db, "INSERT INTO settings(key,value) VALUES ('payment_mode',?)", []byte(`"test"`))
	a, b := &Tenant{s: s, team: "a"}, &Tenant{s: s, team: "b"}
	if trusted {
		for _, tenant := range []*Tenant{a, b} {
			if _, _, err := tenant.TestOnlySetBillingTrust(t.Context(), BillingTrustChange{Trust: BillingTrustGranted, Actor: "ops", Reason: "reviewed"}, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s, a, b, &now
}

func TestPaymentCheckoutRawGrantCannotSettleProviderSession(t *testing.T) {
	s, a, _, now := checkoutCapFixture(t, false)
	id, err := a.TestOnlyOpenCreditCheckout(t.Context(), 100*MicroCreditsPerCent, *now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.TestOnlyAttachCreditCheckout(t.Context(), id, "provider-session", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantPaid, AmountMicro: MicroCreditsPerCent, Reference: "provider-session", CreatedBy: "billing"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("raw paid grant: %v", err)
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 0)
	var unpaid int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM credit_checkouts WHERE team='a' AND id=? AND paid_at IS NULL AND grant_id IS NULL", id).Scan(&unpaid); err != nil || unpaid != 1 {
		t.Fatalf("raw grant settled checkout: unpaid=%d: %v", unpaid, err)
	}
}
