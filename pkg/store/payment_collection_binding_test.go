package store

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestCollectionBindingCommitsWithAttempt(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	cardCollectionDebt(t, s, "debt", 15000)
	work := cardCollectionDue(t, s, now)
	for _, boundary := range []string{"BEFORE INSERT ON payment_correlations", "BEFORE UPDATE OF payment_intent ON card_attempts"} {
		rewriteSchemaExec(t, s.db, "CREATE TRIGGER refuse_collection_binding "+boundary+` BEGIN SELECT RAISE(ABORT,'binding unavailable'); END`)
		before := paymentMutationSnapshot(t, s)
		if err := s.AsOperator().RecordAttemptIntent(t.Context(), work.AttemptID, "payment", work.Customer, now); err == nil {
			t.Fatalf("partial binding committed: %s", boundary)
		}
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM payment_correlations").Scan(&count); err != nil || count != 0 || !reflect.DeepEqual(before, paymentMutationSnapshot(t, s)) {
			t.Fatalf("refused binding changed persistence: correlations=%d: %v", count, err)
		}
		rewriteSchemaExec(t, s.db, "DROP TRIGGER refuse_collection_binding")
	}
	if err := s.AsOperator().RecordAttemptIntent(t.Context(), work.AttemptID, "payment", work.Customer, now); err != nil {
		t.Fatal(err)
	}
	var mode, kind, customer, currency, attempt, charge string
	var team Team
	if err := s.db.QueryRow(`SELECT payment_mode,kind,team,customer_id,currency,attempt_id,charge_id FROM payment_correlations WHERE provider_id='payment'`).Scan(&mode, &kind, &team, &customer, &currency, &attempt, &charge); err != nil {
		t.Fatal(err)
	}
	if mode != "test" || kind != "collection" || team != work.Team || customer != work.Customer || currency != "usd" || attempt != work.AttemptID || charge != work.ChargeID {
		t.Fatalf("association differs from committed collection: %s %s %s %s %s %s %s", mode, kind, team, customer, currency, attempt, charge)
	}
	before := paymentMutationSnapshot(t, s)
	blobAccountingDiskStore(t, s)
	if err := s.AsOperator().RecordAttemptIntent(t.Context(), work.AttemptID, "payment", work.Customer, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, paymentMutationSnapshot(t, s)) {
		t.Fatal("binding replay changed financial state")
	}
}

func TestCollectionBindingRetainsCustomerAfterCardReplacement(t *testing.T) {
	s, tenant, now := providerPaymentFixture(t)
	cardCollectionDebt(t, s, "debt", 15000)
	work := cardCollectionDue(t, s, now)
	if err := tenant.TestOnlySaveCard(t.Context(), Card{Customer: "replacement-customer", PaymentMethod: "replacement-method", Fingerprint: "replacement-card", AddedAt: now.Add(time.Second)}, "billing", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.AsOperator().RecordAttemptIntent(t.Context(), work.AttemptID, "payment", work.Customer, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	event := providerPaymentEvent(now, capability.PaymentCardPaid)
	event.ObjectID, event.AmountMinor = event.PaymentID, work.AmountCents
	event.CustomerID, event.MethodID, event.Status, event.Fingerprint = work.Customer, work.PaymentMethod, "succeeded", "fp_1"
	got, err := s.settlePaymentFacts(t.Context(), event)
	if err != nil || got.Team != tenant.team || got.AmountMicro != event.AmountMinor*MicroCreditsPerCent {
		t.Fatalf("saved-card replacement changed collection: %+v: %v", got, err)
	}
}

func TestCollectionBindingRefusesCheckoutPayment(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	providerCheckoutPaid(t, s)
	cardCollectionDebt(t, s, "debt", 15000)
	work := cardCollectionDue(t, s, now)
	before := paymentMutationSnapshot(t, s)
	if err := s.AsOperator().RecordAttemptIntent(t.Context(), work.AttemptID, "payment", work.Customer, now); err == nil {
		t.Fatal("checkout payment authorized a collection")
	}
	if !reflect.DeepEqual(before, paymentMutationSnapshot(t, s)) {
		t.Fatal("refused checkout payment changed finances")
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM payment_correlations").Scan(&count); err != nil || count != 0 {
		t.Fatalf("refused checkout payment created an association: %d: %v", count, err)
	}
}

func TestCollectionBindingCannotChangePayerProperty(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	cardCollectionDebt(t, s, "debt", 15000)
	work := cardCollectionDue(t, s, now)
	if err := s.AsOperator().RecordAttemptIntent(t.Context(), work.AttemptID, "payment", work.Customer, now); err != nil {
		t.Fatal(err)
	}
	before := paymentMutationSnapshot(t, s)
	property := func(seed uint32, field uint8) bool {
		column, value := "team", "b"
		payment, customer := "payment", work.Customer
		switch field % 8 {
		case 1:
			column, value = "attempt_id", fmt.Sprintf("other-%d", seed)
		case 2:
			column, value = "charge_id", fmt.Sprintf("other-%d", seed)
		case 3:
			column, value = "kind", "setup"
		case 4:
			column, value = "currency", "eur"
		case 5:
			column, value, payment = "team", string(work.Team), fmt.Sprintf("other-%d", seed)
		case 6:
			column, value = "team", string(work.Team)
			rewriteSchemaExec(t, s.db, `UPDATE settings SET value='"live"' WHERE key='payment_mode'`)
			defer rewriteSchemaExec(t, s.db, `UPDATE settings SET value='"test"' WHERE key='payment_mode'`)
		case 7:
			column, value, customer = "team", string(work.Team), fmt.Sprintf("other-%d", seed)
		}
		rewriteSchemaExec(t, s.db, "UPDATE payment_correlations SET "+column+"=? WHERE payment_mode='test' AND provider_id='payment'", value)
		defer rewriteSchemaExec(t, s.db, `UPDATE payment_correlations SET team=?,attempt_id=?,charge_id=?,kind='collection',currency='usd' WHERE payment_mode='test' AND provider_id='payment'`, work.Team, work.AttemptID, work.ChargeID)
		err := s.AsOperator().RecordAttemptIntent(t.Context(), work.AttemptID, payment, customer, now.Add(time.Second))
		var count int
		if queryErr := s.db.QueryRow("SELECT COUNT(*) FROM payment_correlations").Scan(&count); queryErr != nil {
			t.Fatal(queryErr)
		}
		return err != nil && count == 1 && reflect.DeepEqual(before, paymentMutationSnapshot(t, s))
	}
	for field := range uint8(8) {
		if !property(0, field) {
			t.Fatalf("changed binding accepted: field=%d", field)
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32, Rand: rand.New(rand.NewSource(746))}); err != nil {
		t.Fatal(err)
	}
}
