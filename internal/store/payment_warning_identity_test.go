package store

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
)

func TestPaymentWarningCannotChangePaymentProperty(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES ('checkout-b','b','session-b','test','usd',100,1000000,1,2)`)
	other := event
	other.EventID, other.CheckoutID, other.PaymentID = "event-b", "session-b", "payment-b"
	if _, err := s.settlePaymentFacts(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	index := 0
	property := func(seed uint64) bool {
		index++
		warning := paymentWarning{WarningID: fmt.Sprintf("warning-%d-%x", index, seed), PaymentIntent: event.PaymentID, Actionable: true}
		owner, stranger := Team("a"), Team("b")
		if seed&1 != 0 {
			warning.PaymentIntent, owner, stranger = other.PaymentID, stranger, owner
		}
		now := time.Unix(1000, int64(index)).UTC()
		if team, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), warning, now); err != nil || team != owner {
			t.Logf("first warning: %q, %v", team, err)
			return false
		}
		changed := warning
		changed.PaymentIntent = other.PaymentID
		if owner == "b" {
			changed.PaymentIntent = event.PaymentID
		}
		if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), changed, now); !errors.Is(err, ErrCreditGrantConflict) {
			t.Logf("changed warning accepted: %+v, %v", changed, err)
			return false
		}
		var foreign int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM credit_freezes WHERE team=? AND reason=?`, stranger, "early fraud warning "+warning.WarningID).Scan(&foreign); err != nil || foreign != 0 {
			t.Logf("foreign freezes: %d, %v", foreign, err)
			return false
		}
		if team, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), warning, now); err != nil || team != owner {
			t.Logf("exact replay: %q, %v", team, err)
			return false
		}
		return true
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(42))})); err != nil {
		t.Fatal(err)
	}
}

func TestPaymentWarningHoldAndAuditRollbackAndReplayAfterRestart(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	path := blobAccountingDiskStore(t, s)
	warning := paymentWarning{WarningID: "warning", PaymentIntent: event.PaymentID, Actionable: true}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_warning_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), warning, s.now()); err == nil {
		t.Fatal("warning committed without its audit")
	}
	rewriteSchemaCount(t, s.db, "payment_warnings", 0)
	rewriteSchemaCount(t, s.db, "credit_freezes", 0)
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_warning_audit`)
	if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), warning, s.now()); err != nil {
		t.Fatal(err)
	}
	var hold string
	if err := s.db.QueryRow(`SELECT dispute_id FROM credit_freezes`).Scan(&hold); err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	if count, err := tenant.ReleaseCreditFreezes(t.Context(), hold, "console", "reviewed"); err != nil || count != 1 {
		t.Fatalf("release: %d, %v", count, err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = blobAccountingDB(t, path)
	if team, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), warning, s.now()); err != nil || team != "a" {
		t.Fatalf("replay after restart: %q, %v", team, err)
	}
	freeze, err := tenant.CreditFreeze(t.Context())
	if err != nil || freeze.Frozen {
		t.Fatalf("replay restored a released hold: %+v, %v", freeze, err)
	}
	warning.PaymentIntent = "another-payment"
	if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), warning, s.now()); !errors.Is(err, ErrCreditGrantConflict) {
		t.Fatalf("restart lost warning identity: %v", err)
	}
	rewriteSchemaCount(t, s.db, "payment_warnings", 1)
	rewriteSchemaCount(t, s.db, "credit_freezes", 1)
}

func TestPaymentWarningHoldDoesNotReplaceUnrelatedDispute(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	warning := paymentWarning{WarningID: "warning", PaymentIntent: event.PaymentID, Actionable: true}
	if _, err := tenant.TestOnlyHoldForDispute(t.Context(), warning.WarningID, event.PaymentID, "disputed", s.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), warning, s.now()); err != nil {
		t.Fatal(err)
	}
	freeze, err := tenant.CreditFreeze(t.Context())
	if err != nil || !freeze.Frozen || len(freeze.Disputes) != 2 {
		t.Fatalf("independent holds: %+v, %v", freeze, err)
	}
	for _, id := range freeze.Disputes {
		if id == warning.WarningID {
			continue
		}
		if count, err := tenant.ReleaseCreditFreezes(t.Context(), id, "console", "reviewed"); err != nil || count != 1 {
			t.Fatalf("warning release: %d, %v", count, err)
		}
	}
	freeze, err = tenant.CreditFreeze(t.Context())
	if err != nil || !freeze.Frozen || len(freeze.Disputes) != 1 || freeze.Disputes[0] != warning.WarningID {
		t.Fatalf("warning release changed the unrelated dispute: %+v, %v", freeze, err)
	}
}
