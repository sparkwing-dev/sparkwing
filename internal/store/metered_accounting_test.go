package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func meteringFixture(t *testing.T, unit capability.UnitKind, start capability.BillingStart) (*Store, capability.Capability) {
	t.Helper()
	s := authorityStore(t)
	s.clock = func() time.Time { return time.Unix(1000, 0).UTC() }
	rewriteSchemaExec(t, s.db, "INSERT INTO credit_grants(reference,id,team,kind,amount_micro,created_at) VALUES ('fund','fund','a','paid',1000000,1)")
	b := capability.BillingJob
	switch unit {
	case capability.UnitTrigger:
		b = capability.BillingHost
	case capability.UnitPlan:
		b = capability.BillingPlan
	case capability.UnitEval:
		b = capability.BillingEval
	}
	c := authorityRoot("award", "a")
	c.RootMetered = true
	c.Unit = unit
	c.Binding = capability.Binding{RunID: "run", NodeID: "job", Generation: 7, Holder: "machine"}
	c.Billing = &capability.BillingProvenance{PayerTeam: "a", Metered: true, ExecutorClass: "cloud", ReservationID: "reservation", Start: start, Unit: b}
	return s, c
}

func reserveMetering(t *testing.T, s *Store, c capability.Capability) {
	t.Helper()
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return recordBillingAwardTx(t.Context(), tx, c, CreditRate{Cores: 2, MicroPerSecond: 10}, 60)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreDataMeteredReservationImmutableAndAtomic(t *testing.T) {
	s, c := meteringFixture(t, capability.UnitNode, capability.BillingAtStart)
	reserveMetering(t, s, c)
	reserveMetering(t, s, c)
	rewriteSchemaCount(t, s.db, "billing_awards", 1)
	rewriteSchemaCount(t, s.db, "credit_charges", 1)
	c.Billing.ExecutorClass = "forged"
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return recordBillingAwardTx(t.Context(), tx, c, CreditRate{Cores: 2, MicroPerSecond: 10}, 60)
	}); err == nil {
		t.Fatal("award provenance rewritten")
	}
	var balance int64
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		var err error
		balance, err = creditBalanceTx(t.Context(), tx, "a")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if balance != 999400 {
		t.Fatalf("reserved balance=%d", balance)
	}
}

func TestStoreDataMeteredUnstartedRefundAndReplay(t *testing.T) {
	s, c := meteringFixture(t, capability.UnitNode, capability.BillingAtStart)
	reserveMetering(t, s, c)
	for range 2 {
		if err := s.inTx(t.Context(), func(tx *storeTx) error {
			_, err := accountBillingAwardTx(t.Context(), tx, "a", "reservation", s.now(), true)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	rewriteSchemaCount(t, s.db, "credit_charges", 2)
	var amount int64
	if err := s.db.QueryRow("SELECT SUM(amount_micro) FROM credit_charges").Scan(&amount); err != nil || amount != 0 {
		t.Fatalf("unstarted cost=%d, %v", amount, err)
	}
}

func TestStoreDataMeteredIntervalsExcludeWaitAndSettleAfterAuthorityPruning(t *testing.T) {
	s, c := meteringFixture(t, capability.UnitTrigger, capability.BillingAtAward)
	reserveMetering(t, s, c)
	at := s.now()
	s.clock = func() time.Time { return at.Add(3 * time.Hour) }
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return finishBillableIntervalTx(t.Context(), tx, "a", "reservation", "award", at.Add(5*time.Second))
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return startBillableIntervalTx(t.Context(), tx, "a", "reservation", "resume", 0, at.Add(time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return finishBillableIntervalTx(t.Context(), tx, "a", "reservation", "resume", at.Add(time.Hour+7*time.Second))
	}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM capabilities")
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := accountBillingAwardTx(t.Context(), tx, "a", "reservation", at.Add(2*time.Hour), true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var cost int64
	if err := s.db.QueryRow("SELECT SUM(amount_micro) FROM credit_charges").Scan(&cost); err != nil || cost != 600 {
		t.Fatalf("cost=%d,%v", cost, err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return startBillableIntervalTx(t.Context(), tx, "a", "reservation", "late", 0, at.Add(2*time.Hour))
	}); err == nil {
		t.Fatal("settled award reopened")
	}
}

func TestStoreDataMeteredIntervalFencesAndOverlap(t *testing.T) {
	s, c := meteringFixture(t, capability.UnitNode, capability.BillingAtStart)
	reserveMetering(t, s, c)
	at := s.now()
	start := func(team Team, id string, when time.Time) error {
		return s.inTx(t.Context(), func(tx *storeTx) error {
			return startBillableIntervalTx(t.Context(), tx, team, "reservation", id, 1, when)
		})
	}
	if err := start("a", "first", at); err != nil {
		t.Fatal(err)
	}
	if err := start("a", "first", at); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{start("a", "second", at), start("a", "first", at.Add(time.Second)), start("b", "first", at)} {
		if err == nil {
			t.Fatal("invalid interval accepted")
		}
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return finishBillableIntervalTx(t.Context(), tx, "a", "reservation", "first", at.Add(-time.Second))
	}); err == nil {
		t.Fatal("negative interval accepted")
	}
}

func TestStoreDataMeteredConcurrentReservationsCannotOverspend(t *testing.T) {
	s, c := meteringFixture(t, capability.UnitNode, capability.BillingAtStart)
	rewriteSchemaExec(t, s.db, "UPDATE credit_grants SET amount_micro=1000 WHERE id='fund'")
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{"first", "second"} {
		award := c
		billing := *c.Billing
		billing.ReservationID = id
		award.Prefix, award.Billing = id, &billing
		go func() {
			<-start
			results <- s.inTx(t.Context(), func(tx *storeTx) error {
				return recordBillingAwardTx(t.Context(), tx, award, CreditRate{Cores: 2, MicroPerSecond: 10}, 60)
			})
		}()
	}
	close(start)
	successes, refusals := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
			continue
		}
		var insufficient *InsufficientCreditsError
		if !errors.As(err, &insufficient) {
			t.Fatal(err)
		}
		refusals++
	}
	if successes != 1 || refusals != 1 {
		t.Fatalf("reservations: %d admitted, %d refused", successes, refusals)
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 1)
	rewriteSchemaCount(t, s.db, "credit_charges", 1)
}

func TestStoreDataMeteredHeartbeatChargesOnlyNewCompute(t *testing.T) {
	s, c := meteringFixture(t, capability.UnitNode, capability.BillingAtStart)
	reserveMetering(t, s, c)
	start := s.now()
	s.clock = func() time.Time { return start.Add(time.Hour) }
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return startBillableIntervalTx(t.Context(), tx, "a", "reservation", "execute", 1, start)
	}); err != nil {
		t.Fatal(err)
	}
	for _, elapsed := range []time.Duration{30 * time.Second, 70 * time.Second, 70 * time.Second} {
		if err := s.inTx(t.Context(), func(tx *storeTx) error {
			_, err := accountBillingAwardTx(t.Context(), tx, "a", "reservation", start.Add(elapsed), false)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	rewriteSchemaCount(t, s.db, "credit_charges", 2)
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := accountBillingAwardTx(t.Context(), tx, "a", "reservation", start.Add(75*time.Second), true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var seconds, amount int64
	if err := s.db.QueryRow("SELECT SUM(seconds),SUM(amount_micro) FROM credit_charges").Scan(&seconds, &amount); err != nil || seconds != 75 || amount != 750 {
		t.Fatalf("usage=%d seconds, %d micro, %v", seconds, amount, err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		got, err := accountBillingAwardTx(t.Context(), tx, "a", "reservation", start.Add(80*time.Second), true)
		if got.BillableSeconds != 75 {
			t.Fatalf("settlement replay=%+v", got)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "credit_charges", 3)
}

func TestStoreDataMeteredPauseCannotUndoRenewedCompute(t *testing.T) {
	s, c := meteringFixture(t, capability.UnitPlan, capability.BillingAtAward)
	reserveMetering(t, s, c)
	start := s.now()
	s.clock = func() time.Time { return start.Add(2 * time.Minute) }
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := accountBillingAwardTx(t.Context(), tx, "a", "reservation", start.Add(70*time.Second), false)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return finishBillableIntervalTx(t.Context(), tx, "a", "reservation", "award", start.Add(65*time.Second))
	}); err == nil {
		t.Fatal("pause rewound compute already charged by renewal")
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return finishBillableIntervalTx(t.Context(), tx, "a", "reservation", "award", start.Add(75*time.Second))
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreDataUnmeteredSettlementNeedsNoCalculator(t *testing.T) {
	for _, unit := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		for _, start := range []capability.BillingStart{capability.BillingAtAward, capability.BillingAtStart} {
			t.Run(string(unit)+"/"+string(start), func(t *testing.T) {
				s, award := meteringFixture(t, unit, start)
				award.RootMetered, award.Billing.Metered = false, false
				reserveMetering(t, s, award)
				want := capability.BillingSettlement{ReservationID: award.Billing.ReservationID}
				if err := s.inTx(t.Context(), func(tx *storeTx) error {
					got, err := accountBillingAwardTx(t.Context(), tx, "a", want.ReservationID, s.now(), false)
					if err == nil && got != want {
						t.Fatalf("checkpoint=%+v, want %+v", got, want)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, s.db, "DELETE FROM capabilities")
				for range 2 {
					if err := s.inTx(t.Context(), func(tx *storeTx) error {
						got, err := accountBillingAwardTx(t.Context(), tx, "a", want.ReservationID, s.now(), true)
						if err == nil && got != want {
							t.Fatalf("settlement=%+v, want %+v", got, want)
						}
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				rewriteSchemaCount(t, s.db, "billing_awards WHERE settled_at IS NOT NULL", 1)
				rewriteSchemaCount(t, s.db, "credit_charges", 0)
				rewriteSchemaCount(t, s.db, "billable_intervals", 0)
			})
		}
	}
}
