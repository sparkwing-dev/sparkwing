package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestProtectedWarningFreezesStorageSpending(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	event.LiabilityShift, event.ThreeDS = true, "authenticated"
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
	warning := paymentWarning{WarningID: "protected", PaymentIntent: event.PaymentID, Actionable: true}
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
