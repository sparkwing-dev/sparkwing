package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPaymentPurgeClosureCommitsWithParentErasure(t *testing.T) {
	s, _, _ := providerPaymentFixture(t)
	providerCheckoutPaid(t, s)
	op := s.AsOperator()
	if _, _, err := op.RequestTeamDeletion(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if ok, err := op.ClaimTeamDeletion(t.Context(), "a", "purger", time.Hour); err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_parent_erasure BEFORE DELETE ON credit_checkouts BEGIN SELECT RAISE(ABORT,'parent erasure unavailable'); END`)
	if err := op.PurgeTeam(t.Context(), "a", "purger", time.Hour); err == nil {
		t.Fatal("purge ignored failed parent erasure")
	}
	rewriteSchemaCount(t, s.db, "payment_closures", 0)
	route := capability.PaymentRoute{Mode: capability.PaymentTest, PaymentID: "payment"}
	if state, err := op.RecordsPaymentParent(t.Context(), route); err != nil || state != capability.PaymentParentRecorded {
		t.Fatalf("failed purge closed live parent: %v %v", state, err)
	}
	rewriteSchemaCount(t, s.db, "payment_events", 1)
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_parent_erasure`)
	if err := op.PurgeTeam(t.Context(), "a", "purger", time.Hour); err != nil {
		t.Fatal(err)
	}
	if state, err := op.RecordsPaymentParent(t.Context(), route); err != nil || state != capability.PaymentParentPurged {
		t.Fatalf("retry lost closure evidence: %v %v", state, err)
	}
	rewriteSchemaCount(t, s.db, "payment_events", 0)
	rewriteSchemaCount(t, s.db, "credit_checkouts", 0)
}
