package store

import (
	"context"
	"encoding/json"
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

func TestPaymentMutationsRollBackWithReceipt(t *testing.T) {
	for _, kind := range []string{"card_setup", "refund", "dispute_open", "dispute_close", "warning", "queued_refund"} {
		t.Run(kind, func(t *testing.T) {
			s, tenant, now := cardFixture(t)
			event := capability.PaymentEvent{EventID: "event", PaymentID: "payment", Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid}
			if err := s.db.QueryRow("SELECT session_id,currency,amount_minor FROM credit_checkouts WHERE id='checkout'").Scan(&event.CheckoutID, &event.Currency, &event.AmountMinor); err != nil {
				t.Fatal(err)
			}
			if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
				t.Fatal(err)
			}
			if kind == "dispute_close" {
				if _, err := tenant.TestOnlyHoldForDispute(t.Context(), "dispute", event.PaymentID, "disputed", now); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "queued_refund" {
				if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), PaymentWarning{WarningID: "queued", PaymentIntent: event.PaymentID, Actionable: true}, now); err != nil {
					t.Fatal(err)
				}
			}
			mutate := func(tx *storeTx) error {
				ctx := t.Context()
				switch kind {
				case "card_setup":
					return saveCardTx(ctx, tx, tenant.team, Card{Customer: "new-customer", PaymentMethod: "new-method", Fingerprint: "new-card", AddedAt: now.Add(time.Second)}, auditActor{system: auditPaymentProvider}, now)
				case "refund":
					_, err := reversePaymentTx(ctx, tx, event.PaymentID, "refund", auditActor{system: auditPaymentProvider}, 100)
					return err
				case "dispute_open":
					_, err := holdCreditFreezeTx(ctx, tx, tenant.team, "dispute", event.PaymentID, "disputed", "", now, auditActor{system: auditBreakGlass})
					return err
				case "dispute_close":
					_, err := releaseCreditFreezesTx(ctx, tx, tenant.team, "dispute", false, false, "provider", "", now, auditActor{system: auditBreakGlass})
					return err
				case "warning":
					_, err := tx.operator.applyPaymentStateTx(ctx, tx, capability.PaymentEvent{Kind: capability.PaymentWarningEvent, Mode: event.Mode, ObjectID: "warning", PaymentID: event.PaymentID, Fingerprint: "fp_1", Actionable: true}, auditActor{system: auditPaymentProvider})
					return err
				case "queued_refund":
					changed, err := applyCardRefundTx(ctx, tx, tenant.team, event.PaymentID, "provider-refund", "failed", `attempts = ? AND status = 'due'`, []any{0}, now)
					if err == nil && !changed {
						return errors.New("refund queue did not change")
					}
					return err
				}
				return errors.New("unknown payment mutation")
			}
			before := paymentMutationSnapshot(t, s)
			receiptFailure := errors.New("receipt unavailable")
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				if err := mutate(tx); err != nil {
					return err
				}
				return receiptFailure
			}); !errors.Is(err, receiptFailure) {
				t.Fatalf("mutation did not reach receipt failure: %v", err)
			}
			if got := paymentMutationSnapshot(t, s); !reflect.DeepEqual(got, before) {
				t.Fatalf("receipt failure committed %s: before=%v, after=%v", kind, before, got)
			}
			if err := s.inTx(t.Context(), mutate); err != nil {
				t.Fatal(err)
			}
			after := paymentMutationSnapshot(t, s)
			if reflect.DeepEqual(after, before) {
				t.Fatal("committed mutation changed no financial state")
			}
			blobAccountingDiskStore(t, s)
			if got := paymentMutationSnapshot(t, s); !reflect.DeepEqual(got, after) {
				t.Fatalf("mutation did not survive reopening: before=%v, after=%v", after, got)
			}
		})
	}
}

func paymentMutationSnapshot(t *testing.T, s *Store) map[string][]string {
	t.Helper()
	out := make(map[string][]string)
	for _, table := range []string{"teams", "credit_grants", "credit_checkouts", "credit_freezes", "card_refunds", "payment_warnings", "payment_refunds", "audit_events"} {
		rows, err := s.db.QueryContext(t.Context(), "SELECT * FROM "+table)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range targets {
				targets[i] = &values[i]
			}
			if err := rows.Scan(targets...); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			out[table] = append(out[table], string(encoded))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
		slices.Sort(out[table])
	}
	return out
}

func TestPaymentMutationRefundConservationProperty(t *testing.T) {
	for _, mode := range []capability.PaymentMode{capability.PaymentTest, capability.PaymentLive} {
		t.Run(string(mode), func(t *testing.T) {
			property := func(seed uint64) bool {
				s, event := paymentFixture(t, mode)
				if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
					t.Fatal(err)
				}
				paid, reversed := int64(1000000), int64(0)
				for i := range 6 {
					amount := int64((seed>>uint(i*8))%2000000) + 1
					reference := fmt.Sprintf("refund-%d", i)
					mutate := func(tx *storeTx) error {
						result, err := reversePaymentTx(t.Context(), tx, event.PaymentID, reference, auditActor{system: auditPaymentProvider}, amount)
						if err != nil {
							return err
						}
						if result.Team != "a" || result.ReversedMicro < 0 || result.ReversedMicro > paid {
							return fmt.Errorf("invalid payer or reversal: %+v", result)
						}
						_, err = holdCreditFreezeTx(t.Context(), tx, result.Team, reference, event.PaymentID, "disputed", "", s.now(), auditActor{system: auditBreakGlass})
						return err
					}
					failure := errors.New("receipt refused")
					if err := s.inTx(t.Context(), func(tx *storeTx) error {
						if err := mutate(tx); err != nil {
							return err
						}
						return failure
					}); !errors.Is(err, failure) {
						t.Log(err)
						return false
					}
					if !paymentMutationBalances(t.Context(), s, paid-reversed) {
						return false
					}
					for range 2 {
						if err := s.inTx(t.Context(), mutate); err != nil {
							t.Log(err)
							return false
						}
					}
					reversed += min(amount, paid-reversed)
					if !paymentMutationBalances(t.Context(), s, paid-reversed) {
						return false
					}
				}
				return true
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 32, Rand: rand.New(rand.NewSource(42))}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func paymentMutationBalances(ctx context.Context, s *Store, want int64) bool {
	for team, balance := range map[Team]int64{"a": want, "b": 0} {
		got, err := (&Tenant{s: s, team: team}).CreditBalanceMicro(ctx)
		if err != nil || got != balance {
			return false
		}
	}
	return true
}

func TestPaymentMutationCardSetupOrderingProperty(t *testing.T) {
	s, tenant, now := cardFixture(t)
	property := func(seed uint32) bool {
		first := now.Add(time.Duration(seed%1000) * time.Second)
		second := first.Add(time.Duration(seed%999999999 + 1))
		older := Card{Customer: "customer", PaymentMethod: "older", Fingerprint: "older-card", AddedAt: first}
		newer := Card{Customer: "customer", PaymentMethod: "newer", Fingerprint: "newer-card", AddedAt: second}
		for _, card := range []Card{older, newer, newer} {
			if err := tenant.TestOnlySaveCard(t.Context(), card, "provider", second.Add(time.Hour)); err != nil {
				t.Log(err)
				return false
			}
		}
		if err := tenant.TestOnlySaveCard(t.Context(), older, "provider", second.Add(2*time.Hour)); !errors.Is(err, ErrStaleCardSetup) {
			t.Logf("older completion accepted: %v", err)
			return false
		}
		tenant.s.SetTestClock(t, func() time.Time { return second })
		standing, err := tenant.SpendStanding(t.Context())
		if err != nil || standing.Card.PaymentMethod != newer.PaymentMethod || !standing.Card.AddedAt.Equal(second) {
			t.Logf("card or timestamp changed: %+v, %v", standing.Card, err)
			return false
		}
		unordered := newer
		unordered.AddedAt = time.Time{}
		if err := tenant.TestOnlySaveCard(t.Context(), unordered, "provider", first); !errors.Is(err, ErrStaleCardSetup) {
			t.Logf("old receipt bypassed setup order: %v", err)
			return false
		}
		if err := tenant.TestOnlySaveCard(t.Context(), unordered, "provider", second.Add(time.Second)); err != nil {
			t.Log(err)
			return false
		}
		now = second.Add(2 * time.Second)
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32, Rand: rand.New(rand.NewSource(42))}); err != nil {
		t.Fatal(err)
	}
	blobAccountingDiskStore(t, s)
	tenant.s.SetTestClock(t, func() time.Time { return now })
	standing, err := tenant.SpendStanding(t.Context())
	if err != nil || standing.Card.PaymentMethod != "newer" || !standing.Card.AddedAt.Equal(now.Add(-time.Second)) {
		t.Fatalf("setup order did not survive reopening: %+v, %v", standing.Card, err)
	}
}
