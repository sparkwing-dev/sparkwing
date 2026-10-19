package store

import (
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func standingCapFixture(t *testing.T) (*Store, *Tenant, *time.Time) {
	t.Helper()
	s, _, now := billingModelFixture(t)
	rewriteSchemaExec(t, s.db, `DELETE FROM credit_grants`)
	return s, &Tenant{s: s, team: "a"}, now
}

func standingCapPay(t *testing.T, tenant *Tenant, ref string, cents int64) {
	t.Helper()
	if _, err := tenant.TestOnlySettlePaidCredit(t.Context(), CreditGrantRequest{Kind: CreditGrantPaid, AmountMicro: cents * MicroCreditsPerCent, Reference: ref, CreatedBy: "billing"}); err != nil {
		t.Fatal(err)
	}
}

func standingCapFree(t *testing.T, s *Store, id string, cents int64) {
	t.Helper()
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES (?,'a','free',?,1)`, id, cents*MicroCreditsPerCent)
}

func standingCapWant(t *testing.T, tenant *Tenant, now time.Time, tier, history, ceiling string) BillingStanding {
	t.Helper()
	b, err := tenant.BillingStanding(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if b.Tier != tier || b.History.Tier != history || b.PrepayCeiling != ceiling {
		t.Fatalf("standing=%s history=%s ceiling=%s; want %s, %s, %s", b.Tier, b.History.Tier, b.PrepayCeiling, tier, history, ceiling)
	}
	return b
}

func TestStandingCapFreeBalanceLiftsNewOnlyToEstablished(t *testing.T) {
	s, tenant, now := standingCapFixture(t)
	standingCapFree(t, s, "free-high", 50000)
	standingCapWant(t, tenant, *now, "Established", "New", "Established")
}

func TestStandingCapPrepaySettlementReachesTrustedNotHigh(t *testing.T) {
	_, tenant, now := standingCapFixture(t)
	standingCapPay(t, tenant, "pi_high", 50000)
	b := standingCapWant(t, tenant, *now, "Trusted", "Established", "Trusted")
	if b.CloudVCPU != 128 || b.History.PaidMicro != 50000*MicroCreditsPerCent || !b.History.FirstPaidAt.Equal(*now) {
		t.Fatalf("prepaid standing=%+v", b)
	}
	standingCapWant(t, tenant, now.Add(30*24*time.Hour-time.Nanosecond), "Trusted", "Established", "Trusted")
	standingCapWant(t, tenant, now.Add(30*24*time.Hour), "High", "High", "High")
}

func TestStandingCapPrepayNeverReachesHigh(t *testing.T) {
	s, tenant, now := standingCapFixture(t)
	standingCapPay(t, tenant, "pi_trusted", 49999)
	*now = now.Add(30 * 24 * time.Hour)
	standingCapWant(t, tenant, *now, "Trusted", "Trusted", "Trusted")
	standingCapFree(t, s, "free-high", 100000)
	standingCapWant(t, tenant, *now, "Trusted", "Trusted", "Trusted")
	standingCapPay(t, tenant, "pi_history", 1)
	standingCapWant(t, tenant, *now, "High", "High", "High")
}

func TestStandingCapIncidentRestartsHighCleanAge(t *testing.T) {
	s, tenant, now := standingCapFixture(t)
	standingCapPay(t, tenant, "pi_incident", 50000)
	incident := now.Add(29 * 24 * time.Hour)
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_freezes(dispute_id,team,reason,created_at,released_at) VALUES ('dp_old','a','warning',?,?)`, incident.UnixNano(), incident.UnixNano())
	standingCapWant(t, tenant, now.Add(30*24*time.Hour), "Trusted", "Established", "Trusted")
	standingCapWant(t, tenant, incident.Add(30*24*time.Hour), "High", "High", "High")
}

func TestStandingCapGrantAndHoldOverrideTheCap(t *testing.T) {
	_, tenant, now := standingCapFixture(t)
	if _, _, err := tenant.SetBillingTrust(t.Context(), BillingTrustChange{Trust: BillingTrustGranted, Actor: "operator", Reason: "reviewed"}, *now); err != nil {
		t.Fatal(err)
	}
	standingCapWant(t, tenant, *now, "High", "New", "Established")
	if _, _, err := tenant.SetBillingTrust(t.Context(), BillingTrustChange{Trust: BillingTrustRevoked, Actor: "operator", Reason: "fraud"}, *now); err != nil {
		t.Fatal(err)
	}
	standingCapWant(t, tenant, *now, "Held", "New", "Established")
}

func TestStandingCapAdmissionUsesTheCappedTier(t *testing.T) {
	s, tenant, _ := standingCapFixture(t)
	standingCapPay(t, tenant, "pi_admit", 50000)
	err := s.inTx(t.Context(), func(tx *storeTx) error { return checkCloudCapacityTx(t.Context(), tx, "a", 129) })
	if err == nil {
		t.Fatal("prepaid funds admitted a High-sized Cloud job without High history")
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error { return checkCloudCapacityTx(t.Context(), tx, "a", 128) }); err != nil {
		t.Fatalf("Trusted job refused: %v", err)
	}
}

func TestStandingCapPrepayStaysWithinOneTierAndBelowHighProperty(t *testing.T) {
	names := map[string]int{}
	for _, row := range billingPolicy {
		if row.tier != "" {
			names[row.tier] = len(names)
		}
	}
	s, tenant, now := standingCapFixture(t)
	check := func(paidCents, freeCents uint32, ageDays uint8) bool {
		rewriteSchemaExec(t, s.db, `DELETE FROM credit_grants`)
		if paidCents%100000 > 0 {
			rewriteSchemaExec(t, s.db, `INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES ('paid-property','a','paid',?,?)`, int64(paidCents%100000)*MicroCreditsPerCent, now.UnixNano())
		}
		if freeCents%200000 > 0 {
			standingCapFree(t, s, "free-property", int64(freeCents%200000))
		}
		b, err := tenant.BillingStanding(t.Context(), now.Add(time.Duration(ageDays%60)*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		standing, history := names[b.Tier], names[b.History.Tier]
		ceiling := history
		if history < names["Trusted"] {
			ceiling = history + 1
		}
		return standing >= history && standing <= ceiling && names[b.PrepayCeiling] == ceiling
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestStandingCapPaidCheckoutRecordsThreeDSecure(t *testing.T) {
	s, tenant, now := standingCapFixture(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
	checkout, err := tenant.TestOnlyOpenCreditCheckout(t.Context(), 2000*MicroCreditsPerCent, *now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.TestOnlyAttachCreditCheckout(t.Context(), checkout, "3ds-session", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.settlePaymentFacts(t.Context(), capability.PaymentEvent{Mode: capability.PaymentTest, EventID: "3ds", CheckoutID: "3ds-session", PaymentID: "pi_3ds", Kind: capability.PaymentCheckoutPaid, AmountMinor: 2000, Currency: "usd", ThreeDS: "authenticated"}); err != nil {
		t.Fatal(err)
	}
	var recorded, attrs string
	if err := s.db.QueryRow(`SELECT three_d_secure FROM credit_checkouts WHERE id=?`, checkout).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT attrs FROM business_events WHERE team='a' AND kind=?`, BusinessEventCheckoutPaid).Scan(&attrs); err != nil {
		t.Fatal(err)
	}
	if recorded != "authenticated" || !strings.Contains(attrs, `"three_d_secure":"authenticated"`) {
		t.Fatalf("3-D Secure outcome column=%q event=%s", recorded, attrs)
	}
}
