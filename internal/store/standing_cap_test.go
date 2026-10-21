package store

import (
	"errors"
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
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_freezes(dispute_id,team,reason,cause,created_at,released_at) VALUES ('dp_old','a','warning','dispute',?,?)`, incident.UnixNano(), incident.UnixNano())
	standingCapWant(t, tenant, now.Add(30*24*time.Hour), "Trusted", "Established", "Trusted")
	standingCapWant(t, tenant, incident.Add(30*24*time.Hour), "High", "High", "High")
}

func TestStandingCapGrantAndHoldOverrideTheCap(t *testing.T) {
	_, tenant, now := standingCapFixture(t)
	if _, _, err := tenant.TestOnlySetBillingTrust(t.Context(), BillingTrustChange{Trust: BillingTrustGranted, Actor: "operator", Reason: "reviewed"}, *now); err != nil {
		t.Fatal(err)
	}
	standingCapWant(t, tenant, *now, "High", "New", "Established")
	if _, _, err := tenant.TestOnlySetBillingTrust(t.Context(), BillingTrustChange{Trust: BillingTrustRevoked, Actor: "operator", Reason: "fraud"}, *now); err != nil {
		t.Fatal(err)
	}
	standingCapWant(t, tenant, *now, "Held", "New", "Established")
}

func TestStandingCapAdmissionUsesTheCappedTier(t *testing.T) {
	s, tenant, _ := standingCapFixture(t)
	standingCapPay(t, tenant, "pi_admit", 50000)
	err := s.inTx(t.Context(), func(tx *storeTx) error { return cloudCapacityFixture(t, tx, "a", 129) })
	if err == nil {
		t.Fatal("prepaid funds admitted a High-sized Cloud job without High history")
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error { return cloudCapacityFixture(t, tx, "a", 128) }); err != nil {
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
	for _, outcome := range []struct{ provider, audit string }{
		{"authenticated", "authenticated"},
		{"processing_error", "processing_error"},
		{"unknown", "unknown"},
		{"Jane Doe; phone 208-555-0100", "unknown"},
	} {
		t.Run(outcome.audit+"_"+outcome.provider, func(t *testing.T) {
			s, tenant, now := standingCapFixture(t)
			rewriteSchemaExec(t, s.db, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
			checkout, err := tenant.TestOnlyOpenCreditCheckout(t.Context(), 2000*MicroCreditsPerCent, *now, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if err := tenant.TestOnlyAttachCreditCheckout(t.Context(), checkout, "3ds-session", now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.settlePaymentFacts(t.Context(), capability.PaymentEvent{Mode: capability.PaymentTest, EventID: "3ds", CheckoutID: "3ds-session", PaymentID: "pi_3ds", Kind: capability.PaymentCheckoutPaid, AmountMinor: 2000, Currency: "usd", ThreeDS: outcome.provider}); err != nil {
				t.Fatal(err)
			}
			var recorded, attrs string
			if err := s.db.QueryRow(`SELECT three_d_secure FROM credit_checkouts WHERE id=?`, checkout).Scan(&recorded); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT attrs FROM audit_events WHERE team='a' AND kind=?`, BusinessEventCheckoutPaid).Scan(&attrs); err != nil {
				t.Fatal(err)
			}
			if recorded != outcome.provider || !strings.Contains(attrs, `"three_d_secure":"`+outcome.audit+`"`) || strings.Contains(attrs, "Jane Doe") {
				t.Fatalf("3-D Secure outcome column=%q event=%s", recorded, attrs)
			}
		})
	}
}

func standingCapOperatorHold(t *testing.T, s *Store, id string, placed, released time.Time, noFault bool) {
	t.Helper()
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_freezes(dispute_id,team,reason,cause,placed_by,created_at) VALUES (?,'a','review','operator','operator',?)`, id, placed.UnixNano())
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := releaseCreditFreezesTx(t.Context(), tx, "a", id, false, noFault, "operator", "checked", released)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStandingCapNoFaultOperatorReleaseKeepsCleanDays(t *testing.T) {
	_, tenant, now := standingCapFixture(t)
	standingCapPay(t, tenant, "pi_nofault", 50000)
	s := tenant.s
	standingCapOperatorHold(t, s, OperatorHoldPrefix+"nofault", now.Add(10*24*time.Hour), now.Add(12*24*time.Hour), true)
	standingCapWant(t, tenant, now.Add(30*24*time.Hour-time.Nanosecond), "Trusted", "Established", "Trusted")
	standingCapWant(t, tenant, now.Add(30*24*time.Hour), "High", "High", "High")
	standingCapOperatorHold(t, s, OperatorHoldPrefix+"fault", now.Add(10*24*time.Hour), now.Add(12*24*time.Hour), false)
	standingCapWant(t, tenant, now.Add(42*24*time.Hour-time.Nanosecond), "Trusted", "Established", "Trusted")
	standingCapWant(t, tenant, now.Add(42*24*time.Hour), "High", "High", "High")
	var noFault int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE team='a' AND kind=? AND attrs LIKE '%"no_fault":true%'`, BusinessEventTeamUnfrozen).Scan(&noFault); err != nil || noFault != 1 {
		t.Fatalf("no-fault audit events=%d: %v", noFault, err)
	}
}

func TestStandingCapNoFaultReleaseOnlyForOperatorHolds(t *testing.T) {
	s, _, now := standingCapFixture(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_freezes(dispute_id,team,reason,cause,created_at) VALUES ('dp_1','a','dispute','dispute',?)`, now.UnixNano())
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := releaseCreditFreezesTx(t.Context(), tx, "a", "dp_1", false, true, "operator", "checked", *now)
		return err
	})
	var open int
	if qerr := s.db.QueryRow(`SELECT COUNT(*) FROM credit_freezes WHERE dispute_id='dp_1' AND released_at IS NULL`).Scan(&open); !errors.Is(err, ErrInvalidInput) || qerr != nil || open != 1 {
		t.Fatalf("no-fault dispute release: %v open=%d %v", err, open, qerr)
	}
}

func TestHeldAdmissionNamesTheBlockingHoldAndItsRemedy(t *testing.T) {
	for _, tc := range []struct {
		holds  [][2]string
		cause  string
		remedy string
	}{
		{[][2]string{{"dp_1", HoldCauseDispute}}, HoldCauseDispute, "payment dispute"},
		{[][2]string{{"efw_1", HoldCauseWarning}}, HoldCauseWarning, "possible fraud"},
		{[][2]string{{OperatorHoldPrefix + "x", HoldCauseOperator}}, HoldCauseOperator, "Sparkwing support"},
		{[][2]string{{"dp_1", HoldCauseDispute}, {"efw_1", HoldCauseWarning}}, HoldCauseDispute, "payment dispute"},
	} {
		s, tenant, now := standingCapFixture(t)
		for _, h := range tc.holds {
			placed := ""
			if h[1] == HoldCauseOperator {
				placed = "console"
			}
			rewriteSchemaExec(t, s.db, `INSERT INTO credit_freezes(dispute_id,team,reason,cause,placed_by,created_at) VALUES (?,'a','r',?,?,1)`, h[0], h[1], placed)
		}
		b := standingCapWant(t, tenant, *now, "Held", "New", "Established")
		if b.HoldCause != tc.cause {
			t.Fatalf("%v: cause=%s", tc.holds, b.HoldCause)
		}
		err := s.inTx(t.Context(), func(tx *storeTx) error { return cloudCapacityFixture(t, tx, "a", 1) })
		var refusal *InsufficientCreditsError
		if !errors.As(err, &refusal) || refusal.Hold != tc.cause || !strings.Contains(err.Error(), tc.remedy) {
			t.Fatalf("%v: refusal %v", tc.holds, err)
		}
	}
}

func cloudCapacityFixture(t *testing.T, tx *storeTx, team Team, cores int64) error {
	t.Helper()
	l, err := lockLedgerTx(t.Context(), tx, team)
	if err != nil {
		return err
	}
	return checkCloudCapacityTx(t.Context(), l, cores)
}
