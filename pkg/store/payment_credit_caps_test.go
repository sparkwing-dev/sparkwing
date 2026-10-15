package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPaymentFreeGrantCapIncludesOtherGrantsAndOpenCheckouts(t *testing.T) {
	t.Run("grants", func(t *testing.T) {
		_, a, b, _ := checkoutCapFixture(t, false)
		request := CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: MaxTeamBalanceMicro - MicroCreditsPerCent, Reference: "gift", CreatedBy: "ops"}
		first, err := a.RecordCreditGrant(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 2 * MicroCreditsPerCent, Reference: "second", CreatedBy: "ops"})
		var capErr *CreditBalanceCapError
		if !errors.Is(err, ErrCreditBalanceCap) || !errors.As(err, &capErr) || capErr.BalanceMicro != request.AmountMicro || capErr.AmountMicro != 2*MicroCreditsPerCent || capErr.CapMicro != MaxTeamBalanceMicro {
			t.Fatalf("free grant cap=%v", err)
		}
		if balance, err := a.CreditBalanceMicro(t.Context()); err != nil || balance != request.AmountMicro {
			t.Fatalf("refused grant changed balance=%d: %v", balance, err)
		}
		if replay, err := a.RecordCreditGrant(t.Context(), request); err != nil || replay.Created || replay.Grant.ID != first.Grant.ID {
			t.Fatalf("grant replay=%+v: %v", replay, err)
		}
		request.AmountMicro = MaxTeamBalanceMicro
		if _, err := b.RecordCreditGrant(t.Context(), request); err != nil {
			t.Fatalf("another team's grant consumed this cap: %v", err)
		}
	})
	t.Run("checkout", func(t *testing.T) {
		_, a, _, now := checkoutCapFixture(t, true)
		const usd = 100 * MicroCreditsPerCent
		if _, err := a.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 4000 * usd, Reference: "gift", CreatedBy: "ops"}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.OpenCreditCheckout(t.Context(), 500*usd, *now, 31*time.Minute); err != nil {
			t.Fatal(err)
		}
		_, err := a.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1000 * usd, Reference: "second", CreatedBy: "ops"})
		var capErr *CreditBalanceCapError
		if !errors.As(err, &capErr) || capErr.OpenMicro != 500*usd {
			t.Fatalf("free grant consumed checkout's hold: %v", err)
		}
		if _, err := a.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 500 * usd, Reference: "third", CreatedBy: "ops"}); err != nil {
			t.Fatal(err)
		}
	})
}

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

func TestPaymentConcurrentCheckoutClientsCannotBothPassCap(t *testing.T) {
	s, a, _, now := checkoutCapFixture(t, true)
	path := blobAccountingDiskStore(t, s)
	other := &Tenant{s: &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}, team: "a"}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, tenant := range []*Tenant{a, other} {
		go func() {
			<-start
			_, err := tenant.OpenCreditCheckout(t.Context(), MaxTeamBalanceMicro/2+MicroCreditsPerCent, *now, 31*time.Minute)
			results <- err
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrCreditBalanceCap) {
			t.Error(err)
		}
	}
	if winners != 1 {
		t.Fatalf("checkout winners=%d", winners)
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts", 1)
}

func TestStoreDataMeteredUsageByRunNetsHostJobAndRefunds(t *testing.T) {
	s, award := meteringFixture(t, capability.UnitNode, capability.BillingAtStart)
	rewriteSchemaExec(t, s.db, "INSERT INTO credit_grants(id,team,kind,amount_micro,created_at) VALUES ('other-fund','b','paid',1000000,1)")
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
			_, err := accountBillingAwardTx(t.Context(), tx, tc.team, tc.id, now, true, func(p capability.BillingProvenance, _ []capability.BillableInterval) (capability.BillingSettlement, error) {
				return capability.BillingSettlement{ReservationID: p.ReservationID, BillableSeconds: tc.seconds}, nil
			})
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
