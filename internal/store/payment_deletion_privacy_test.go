package store

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func assertPurgedFinancialPrivacy(t *testing.T, s *Store, team Team) {
	t.Helper()
	tables := append(financialTables(), "payment_events", "payment_correlations", "console_refund_receipts")
	for _, table := range schemaTables {
		if table.RetainOnTeamDeletion && !slices.Contains(tables, table.Name) {
			tables = append(tables, table.Name)
		}
	}
	for _, table := range tables {
		query, args := "SELECT * FROM "+table, []any(nil)
		if table == "payment_setups" {
			query, args = query+` WHERE EXISTS (SELECT 1 FROM payment_correlations c WHERE c.team=? AND c.payment_mode=payment_setups.payment_mode AND c.provider_id=payment_setups.intent_id)`, []any{team}
		} else if table == "payment_states" {
			query, args = query+` WHERE EXISTS (SELECT 1 FROM credit_checkouts c WHERE c.team=? AND c.payment_mode=payment_states.payment_mode AND c.payment_id=payment_states.payment_id)`, []any{team}
		} else if table == "payment_events" {
			query, args = query+` WHERE team=? OR EXISTS (SELECT 1 FROM payment_correlations c WHERE c.team=? AND c.payment_mode=payment_events.payment_mode AND c.provider_id=payment_events.object_id)`, []any{team, team}
		} else if sqliteColumns(t, s, table)["team"] {
			query, args = query+" WHERE team=?", []any{team}
		}
		rows, err := s.db.QueryContext(t.Context(), rewritePh(s.dialect, query), args...)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			for i, column := range columns {
				if values[i] == nil {
					continue
				}
				raw := fmt.Sprint(values[i])
				if b, ok := values[i].([]byte); ok {
					raw = string(b)
				}
				if slices.Contains([]string{"payment_method", "brand", "last4", "receipt_email"}, column) && raw != "" {
					t.Errorf("%s.%s retained card or receipt data: %s", table, column, raw)
				}
				if column == "event_json" || column == "outcome_json" {
					var event capability.PaymentEvent
					if err := json.Unmarshal([]byte(raw), &event); err != nil {
						t.Fatal(err)
					}
					if event.MethodID != "" || event.Fingerprint != "" || event.Brand != "" || event.Last4 != "" {
						t.Errorf("%s retained card details: %+v", table, event)
					}
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLatePaymentCannotRestorePurgedCardDetails(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	paid := providerRefillEvent(t, s, now, capability.PaymentCardPaid)
	paid.MethodID, paid.Brand, paid.Last4 = "private-method", "private-brand", "9999"
	purgePaymentTeam(t, s, "a")
	for range 2 {
		if _, err := s.settlePaymentFacts(t.Context(), paid); err != nil {
			t.Fatal(err)
		}
		assertPurgedFinancialPrivacy(t, s, "a")
	}
}

func TestTeamPurgeRemovesRetainedSavedCardCopies(t *testing.T) {
	s, _, now := providerPaymentFixture(t)
	paid := providerRefillEvent(t, s, now, capability.PaymentCardPaid)
	paid.MethodID, paid.Brand, paid.Last4 = "private-method", "private-brand", "9999"
	if _, err := s.settlePaymentFacts(t.Context(), paid); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id) VALUES ('test','setup','setup','a','customer')`)
	setup := capability.PaymentEvent{EventID: "setup-event", ObjectID: "setup", Mode: capability.PaymentTest, Kind: capability.PaymentCardSetup, CustomerID: "customer", MethodID: "private-method", Fingerprint: "private-card", Brand: "private-brand", Last4: "9999", Created: now.Unix() + 1}
	if _, err := s.settlePaymentFacts(t.Context(), setup); err != nil {
		t.Fatal(err)
	}
	// Setup callbacks can arrive before the completed page binds their payer.
	unbound := setup
	unbound.EventID, unbound.ObjectID = "unbound-setup-event", "unbound-setup"
	if _, err := s.settlePaymentFacts(t.Context(), unbound); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id) VALUES ('test','unbound-setup','setup','a','customer'),('test','other-setup','setup','b','customer')`)
	other := setup
	other.EventID, other.ObjectID = "other-setup-event", "other-setup"
	if _, err := s.settlePaymentFacts(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	purgePaymentTeam(t, s, "a")
	rewriteSchemaCount(t, s.db, "payment_setups", 1)
	for _, event := range []capability.PaymentEvent{setup, unbound, other} {
		var encoded string
		if err := s.queryRow(t.Context(), `SELECT event_json FROM payment_events WHERE event_id=?`, event.EventID).Scan(&encoded); err != nil {
			t.Fatal(err)
		}
		var retained capability.PaymentEvent
		if err := json.Unmarshal([]byte(encoded), &retained); err != nil {
			t.Fatal(err)
		}
		if event != other {
			event.MethodID, event.Fingerprint, event.Brand, event.Last4 = "", "", "", ""
		}
		if retained != event {
			t.Errorf("setup facts changed: got %+v, want %+v", retained, event)
		}
	}
	var encoded string
	if err := s.queryRow(t.Context(), `SELECT event_json FROM payment_events WHERE event_id=?`, paid.EventID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var retained capability.PaymentEvent
	if err := json.Unmarshal([]byte(encoded), &retained); err != nil {
		t.Fatal(err)
	}
	paid.MethodID, paid.Fingerprint, paid.Brand, paid.Last4 = "", "", "", ""
	if retained != paid {
		t.Fatalf("purge changed financial facts: got %+v, want %+v", retained, paid)
	}
}
