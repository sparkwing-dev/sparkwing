package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPaymentPaidEventsPreserveFundsAboveBalanceCap(t *testing.T) {
	s, a, _, _ := checkoutCapFixture(t, true)
	const usd = 100 * MicroCreditsPerCent
	if _, err := a.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 4600 * usd, Reference: "gift", CreatedBy: "ops"}); err != nil {
		t.Fatal(err)
	}
	path := blobAccountingDiskStore(t, s)
	other := &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, client := range []*Store{s, other} {
		id := []string{"one", "two"}[i]
		rewriteSchemaExec(t, s.db, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES (?,'a',?,'test','usd',30000,?,1,2)`, "checkout-"+id, "session-"+id, 300*usd)
		event := capability.PaymentEvent{Mode: capability.PaymentTest, EventID: "paid-" + id, CheckoutID: "session-" + id, PaymentID: "payment-" + id, Kind: capability.PaymentCheckoutPaid, AmountMinor: 30000, Currency: "usd"}
		go func() {
			<-start
			_, err := client.settlePaymentFacts(t.Context(), event)
			results <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Errorf("verified payment refused: %v", err)
		}
	}
	if balance, err := a.CreditBalanceMicro(t.Context()); err != nil || balance != 5200*usd {
		t.Fatalf("paid funds lost above cap: balance=%d: %v", balance, err)
	}
	rewriteSchemaCount(t, s.db, "payment_events", 2)
}

func TestStoreDataMeteredUsageByRunNetsHostJobAndRefunds(t *testing.T) {
	s, award := meteringFixture(t, capability.UnitNode, capability.BillingAtStart)
	rewriteSchemaExec(t, s.db, "INSERT INTO credit_grants(reference,id,team,kind,amount_micro,created_at) VALUES ('other-fund','other-fund','b','paid',1000000,1)")
	now := s.now()
	s.clock = func() time.Time { return now }
	for _, tc := range []struct {
		team    Team
		run, id string
		unit    capability.UnitKind
		billing capability.BillingUnit
		seconds int64
	}{
		{"a", "one", "job", capability.UnitNode, capability.BillingJob, 60},
		{"a", "one", "host", capability.UnitTrigger, capability.BillingHost, 60},
		{"a", "two", "plan", capability.UnitPlan, capability.BillingPlan, 3},
		{"b", "foreign", "other", capability.UnitNode, capability.BillingJob, 60},
	} {
		c := award
		b := *award.Billing
		b.ReservationID, b.PayerTeam, b.Unit = tc.id, string(tc.team), tc.billing
		c.Team, c.Prefix, c.Unit, c.Binding.RunID, c.Billing = string(tc.team), tc.id, tc.unit, tc.run, &b
		reserveMetering(t, s, c)
		if err := s.inTx(t.Context(), func(tx *storeTx) error {
			return startBillableIntervalTx(t.Context(), tx, tc.team, tc.id, "execute", 1, now)
		}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(3 * time.Second)
		if err := s.inTx(t.Context(), func(tx *storeTx) error {
			_, err := accountBillingAwardTx(t.Context(), tx, tc.team, tc.id, now, true)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := insertStorageChargeTx(t.Context(), tx, storageCharge{Team: "a", Bytes: 100, Seconds: 1, AmountMicro: 9, NowNS: now.UnixNano()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	usage, err := tenant.CreditUsageByRun(t.Context(), 10)
	if err != nil || len(usage) != 2 || usage[0].RunID != "two" || usage[0].Seconds != 3 || usage[0].AmountMicro != 30 || usage[1].RunID != "one" || usage[1].Seconds != 120 || usage[1].AmountMicro != 1200 {
		t.Fatalf("run usage=%+v: %v", usage, err)
	}
	if limited, err := tenant.CreditUsageByRun(t.Context(), 1); err != nil || len(limited) != 1 || limited[0].RunID != "two" {
		t.Fatalf("limited usage=%+v: %v", limited, err)
	}
}
