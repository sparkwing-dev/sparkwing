package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestStoreDataMeteredDisruptionReplayUsesAwardedTerms(t *testing.T) {
	property := func(unitIndex, elapsed, tariff uint8, atAward, started bool) bool {
		name := fmt.Sprintf("unit=%d/at_award=%t/started=%t/elapsed=%d/rate=%d", unitIndex%4, atAward, started, elapsed, tariff)
		return t.Run(name, func(t *testing.T) {
			checkMeteredDisruption(t, unitIndex, elapsed, tariff, atAward, started)
		})
	}
	for unit := range uint8(4) {
		for _, atAward := range []bool{false, true} {
			for _, started := range []bool{false, true} {
				for _, elapsed := range []uint8{6, 59, 60, 239} {
					if !property(unit, elapsed, 9, atAward, started) {
						t.Fatalf("unit=%d elapsed=%d at_award=%v started=%v", unit, elapsed, atAward, started)
					}
				}
			}
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48, Rand: rand.New(rand.NewSource(420738))}); err != nil {
		t.Fatal(err)
	}
}

func checkMeteredDisruption(t *testing.T, unitIndex, elapsed, tariff uint8, atAward, started bool) {
	t.Helper()
	unit := []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode, capability.UnitTrigger}[unitIndex%4]
	mode := capability.BillingAtStart
	if atAward {
		mode = capability.BillingAtAward
	}
	s, award := meteringFixture(t, unit, mode)
	path := blobAccountingDiskStore(t, s)
	root := authorityRoot("root", "a")
	root.RootMetered = true
	authorityPersist(t, s, root)
	price := CreditRate{Cores: 2, MicroPerSecond: int64(tariff) + 1}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return recordBillingAwardTx(t.Context(), tx, award, price, 60)
	}); err != nil {
		t.Fatal(err)
	}
	awarded := s.now()
	began := awarded
	if !atAward {
		began = awarded.Add(20 * time.Second)
	}
	lapse := began.Add((time.Duration(elapsed) + 1) * time.Second)
	s.clock = func() time.Time { return awarded.Add(48 * time.Hour) }
	started = started || atAward
	if started && !atAward {
		if err := s.inTx(t.Context(), func(tx *storeTx) error {
			return startBillableIntervalTx(t.Context(), tx, "a", "reservation", "execute", 1, began)
		}); err != nil {
			t.Fatal(err)
		}
	}
	through := began.Add(lapse.Sub(began) / 2)
	if started {
		if err := s.inTx(t.Context(), func(tx *storeTx) error {
			_, err := accountBillingAwardTx(t.Context(), tx, "a", "reservation", through, false)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	through = lapse
	rewriteSchemaExec(t, s.db, "DELETE FROM capabilities")
	newRate := price.MicroPerSecond + 10000
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateMicroPerSecond: &newRate}); err != nil {
		t.Fatal(err)
	}
	rollbackMeteredDisruption(t, s, lapse)
	raceMeteredDisruptionReplay(t, s, path, lapse)
	wantSeconds := int64(0)
	if started {
		wantSeconds = int64(lapse.Sub(began) / time.Second)
		if unit == capability.UnitNode || unit == capability.UnitTrigger {
			wantSeconds = max(wantSeconds, 60)
		}
	}
	want := capability.BillingSettlement{ReservationID: "reservation", BillableSeconds: wantSeconds, RefundUnstarted: !started}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = blobAccountingDB(t, path)
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		got, err := accountBillingAwardTx(t.Context(), tx, "a", "reservation", s.now(), true)
		if err == nil && got != want {
			return fmt.Errorf("replayed settlement=%+v, want %+v", got, want)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !checkMeteredDisruptionLedger(t, s, price, awarded, wantSeconds) {
		t.Fatal("settlement changed awarded terms")
	}
}

func rollbackMeteredDisruption(t *testing.T, s *Store, lapse time.Time) {
	t.Helper()
	tenant := &Tenant{s: s, team: "a"}
	before, err := tenant.ListCreditCharges(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	intervalsBefore := billingIntervalCheckpoints(t, s)
	refused := errors.New("terminal state write failed")
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		if _, err := accountBillingAwardTx(t.Context(), tx, "a", "reservation", lapse, true); err != nil {
			return err
		}
		return refused
	}); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	after, err := tenant.ListCreditCharges(t.Context(), 100)
	if err != nil || !reflect.DeepEqual(before, after) || !slices.Equal(intervalsBefore, billingIntervalCheckpoints(t, s)) {
		t.Fatalf("failed transaction changed charges or intervals: %+v, %v", after, err)
	}
}

func raceMeteredDisruptionReplay(t *testing.T, s *Store, path string, lapse time.Time) {
	t.Helper()
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = blobAccountingDB(t, path)
	other := &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	start, done := make(chan struct{}), make(chan error, 2)
	for _, client := range []*Store{s, other} {
		go func() {
			<-start
			done <- client.inTx(ctx, func(tx *storeTx) error {
				_, err := accountBillingAwardTx(ctx, tx, "a", "reservation", lapse, true)
				return err
			})
		}()
	}
	close(start)
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func checkMeteredDisruptionLedger(t *testing.T, s *Store, price CreditRate, awarded time.Time, wantSeconds int64) bool {
	t.Helper()
	var amount, seconds, settled int64
	if err := s.db.QueryRow("SELECT SUM(amount_micro),SUM(seconds) FROM credit_charges WHERE team='a'").Scan(&amount, &seconds); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT settled_at FROM billing_awards WHERE team='a' AND reservation_id='reservation'").Scan(&settled); err != nil {
		t.Fatal(err)
	}
	if amount != wantSeconds*price.MicroPerSecond || seconds != wantSeconds || settled != s.now().UnixNano() {
		t.Logf("seconds=%d amount=%d settled=%d; want %d seconds at rate %d", seconds, amount, settled, wantSeconds, price.MicroPerSecond)
		return false
	}
	tenant := &Tenant{s: s, team: "a"}
	charges, err := tenant.ListCreditCharges(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, charge := range charges {
		if charge.RateMicroPerSecond != price.MicroPerSecond || charge.CPUClassCores != price.Cores || (charge.Kind == CreditChargeRefund && !charge.ChargedAt.Equal(awarded)) {
			t.Logf("charge lost awarded terms: %+v", charge)
			return false
		}
	}
	return true
}
