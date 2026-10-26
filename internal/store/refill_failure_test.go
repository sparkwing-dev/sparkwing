package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

// A refill the checkout service could not create for lack of a card closes as no_card, blocks later refills and
// is reported for the owners' notice, once.
func TestARefillWithoutACardClosesAndBlocksRefills(t *testing.T) {
	ctx := t.Context()
	s := storetest.Open(t)
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO teams(name,created_at,updated_at,card_customer,card_added_at,refill_enabled,refill_threshold_cents,refill_amount_cents,billing_trust,billing_trust_at,billing_trust_by,billing_trust_reason) VALUES ('a',1,1,'cus_a',1,1,1000,2000,'granted',1,'operator','customer')`); err != nil {
		t.Fatal(err)
	}
	due, err := collectDueRefills(ctx, s.AsOperator())
	if err != nil || len(due) != 1 {
		t.Fatalf("due = %+v, %v", due, err)
	}
	for i, want := range []bool{false, true} {
		got, err := s.AsOperator().RecordRefillFailed(ctx, due[0], store.RefillNoCard)
		if err != nil || got.Replayed != want || !want && (got.Team != "a" || got.RefillFailed != store.RefillNoCard) {
			t.Fatalf("record %d = %+v, %v", i, got, err)
		}
	}
	if again, err := collectDueRefills(ctx, s.AsOperator()); err != nil || len(again) != 0 {
		t.Fatalf("refills after no_card = %+v, %v", again, err)
	}
	tenant, err := s.ForTeam(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if st, err := tenant.SpendStanding(ctx); err != nil || st.Refill.Failure != store.RefillNoCard || st.Refill.DayCount != 0 {
		t.Fatalf("refill state = %+v, %v; want blocked as no_card without consuming limits", st.Refill, err)
	}
}
