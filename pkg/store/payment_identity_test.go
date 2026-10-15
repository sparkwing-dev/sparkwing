package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPaymentSettlementIdentityCannotFundTwoCheckouts(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES ('other','b','other-session','test','usd',100,1000000,1,2)`)
	event.EventID, event.CheckoutID = "other-event", "other-session"
	if _, err := s.settlePaymentFacts(t.Context(), event); err == nil {
		t.Fatal("same payment funded another team")
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 1)
	rewriteSchemaCount(t, s.db, "payment_events", 1)
}
