package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestProtectedWarningFreezesStorageSpending(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	event.LiabilityShift, event.ThreeDS, event.Fingerprint = true, "authenticated", "fp_protected"
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	now := s.now()
	s.clock = func() time.Time { return now }
	rate := int64(CloudStorageRateMicroPerGBDay)
	if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{StorageRateMicroPerGBDay: &rate}); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "storage", 60*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	retainedBlob(t, s, "a", "a", StorageBytesPerGB, now)
	retainedBill(t, s, lease)
	warning := PaymentWarning{WarningID: "protected", PaymentIntent: event.PaymentID, Fingerprint: event.Fingerprint, Actionable: true}
	if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), warning, now); err != nil {
		t.Fatal(err)
	}
	before, err := tenant.CreditBalanceMicro(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('a','run-a',1,'p','running',?,'request',1,'gh:1','host','execute',?,'account',1,'r30',30)`, []byte(`{"version":1}`), []byte(`[]`))
	if _, err := tenant.AppendEventCharged(t.Context(), "replica", "run-a", "node", "log", []byte("frozen")); !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("storage growth = %v; want held credit refused", err)
	}
	now = now.Add(24 * time.Hour)
	if billed := retainedBill(t, s, lease); len(billed.Charges) != 0 {
		t.Fatalf("frozen credit funded retained storage: %+v", billed)
	}
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != before {
		t.Fatalf("frozen balance = %d, want %d: %v", balance, before, err)
	}
	freeze, err := tenant.CreditFreeze(t.Context())
	if err != nil || len(freeze.Disputes) != 1 {
		t.Fatalf("freeze = %+v: %v", freeze, err)
	}
	s.SetTestClock(t, func() time.Time { return now })
	if _, err := tenant.ReleaseCreditFreezes(t.Context(), freeze.Disputes[0], "operator", "reviewed"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	if billed := retainedBill(t, s, lease); len(billed.Charges) != 1 || billed.ChargedMicro != min(before, rate) {
		t.Fatalf("released storage = %+v; want only the released day charged", billed)
	}
}

func TestProtectedWarningCannotMoveACardToAnotherTeam(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	event.Fingerprint, event.LiabilityShift = "fp_shared", true
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES ('other','b','other-session','test','usd',100,1000000,1,2)`)
	other := event
	other.EventID, other.CheckoutID, other.PaymentID = "other-event", "other-session", "other-payment"
	warning := PaymentWarning{WarningID: "other-warning", PaymentIntent: other.PaymentID, Fingerprint: other.Fingerprint, Actionable: true}
	if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), warning, s.now()); err != nil {
		t.Fatal(err)
	}
	settlement, err := s.settlePaymentFacts(t.Context(), other)
	if err != nil || settlement.Granted || settlement.Refused != CardRefusedPinned {
		t.Fatalf("protected payment bypassed card pin: %+v: %v", settlement, err)
	}
	refunds, err := s.AsOperator().DueCardRefunds(t.Context())
	if err != nil || len(refunds) != 1 || refunds[0].Team != "b" || refunds[0].PaymentIntent != other.PaymentID {
		t.Fatalf("refused payment refunds = %+v: %v", refunds, err)
	}
}

func TestPaymentWarningUsesConfiguredModeWhenTeamHasBothModes(t *testing.T) {
	s, paid := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), paid); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at)
	    VALUES ('live','a','live-session','live','usd',100,1000000,1,2)`)
	if err := s.AsOperator().SetPaymentMode(t.Context(), capability.PaymentLive); err != nil {
		t.Fatal(err)
	}
	paid.Mode, paid.CheckoutID, paid.EventID = capability.PaymentLive, "live-session", "live-paid"
	if _, err := s.settlePaymentFacts(t.Context(), paid); err != nil {
		t.Fatal(err)
	}
	warning := providerPaymentEvent(s.now(), capability.PaymentWarningEvent)
	warning.Mode, warning.PaymentID, warning.Fingerprint, warning.Actionable = capability.PaymentLive, paid.PaymentID, paid.Fingerprint, true
	if _, err := s.settlePaymentFacts(t.Context(), warning); err != nil {
		t.Fatal(err)
	}
	if balance, err := (&Tenant{s: s, team: "a"}).CreditBalanceMicro(t.Context()); err != nil || balance != 1000000 {
		t.Fatalf("live warning reversed wrong mode: balance=%d: %v", balance, err)
	}
	refunds, err := s.AsOperator().DueCardRefunds(t.Context())
	if err != nil || len(refunds) != 1 || refunds[0].PaymentIntent != paid.PaymentID {
		t.Fatalf("live warning refund=%+v: %v", refunds, err)
	}
}
