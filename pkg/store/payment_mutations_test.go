package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"slices"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPaymentMutationsRollBackWithReceipt(t *testing.T) {
	for _, kind := range []string{"card_setup", "refund", "dispute_open", "dispute_close", "warning", "collection_paid", "collection_failed", "queued_refund"} {
		t.Run(kind, func(t *testing.T) {
			s, tenant, now := cardCollectionFixture(t)
			event := capability.PaymentEvent{EventID: "event", PaymentID: "payment", Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid}
			if err := s.db.QueryRow("SELECT session_id,currency,amount_minor FROM credit_checkouts WHERE id='checkout'").Scan(&event.CheckoutID, &event.Currency, &event.AmountMinor); err != nil {
				t.Fatal(err)
			}
			if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
				t.Fatal(err)
			}
			cardCollectionDebt(t, s, "debt", 15000)
			work := cardCollectionDue(t, s, now)
			payment := CardPayment{ChargeID: work.ChargeID, AttemptID: work.AttemptID, PaymentIntent: "collected", AmountCents: work.AmountCents}
			if kind == "dispute_close" {
				if _, err := tenant.HoldForDispute(t.Context(), "dispute", event.PaymentID, "disputed", now); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "queued_refund" {
				payment.AmountCents = 1
				if _, _, err := s.AsOperator().SettleCardPayment(t.Context(), payment, now); err != nil {
					t.Fatal(err)
				}
			}
			mutate := func(tx *storeTx) error {
				ctx := t.Context()
				switch kind {
				case "card_setup":
					return saveCardTx(ctx, tx, tenant.team, Card{Customer: "new-customer", PaymentMethod: "new-method", Fingerprint: "new-card", AddedAt: now.Add(time.Second)}, "provider", now)
				case "refund":
					_, err := reversePaymentTx(ctx, tx, event.PaymentID, "refund", "provider", 100)
					return err
				case "dispute_open":
					_, err := holdCreditFreezeTx(ctx, tx, tenant.team, "dispute", event.PaymentID, "disputed", "", now)
					return err
				case "dispute_close":
					_, err := releaseCreditFreezesTx(ctx, tx, tenant.team, "dispute", false, "provider", "", now)
					return err
				case "warning":
					_, err := recordPaymentWarningTx(ctx, tx, PaymentWarning{WarningID: "warning", PaymentIntent: event.PaymentID, Fingerprint: "fp_1", Actionable: true}, now)
					return err
				case "collection_paid":
					_, _, err := settleCardPaymentTx(ctx, tx, payment, now)
					return err
				case "collection_failed":
					_, _, err := failCardAttemptTx(ctx, tx, work.AttemptID, payment.PaymentIntent, "declined", now)
					return err
				case "queued_refund":
					changed, err := reportCardRefundTx(ctx, tx, payment.PaymentIntent+"-0", "provider-refund", "failed", now)
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
	for _, table := range []string{"teams", "credit_grants", "credit_freezes", "card_charges", "card_attempts", "card_refunds", "payment_warnings", "business_events"} {
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
						result, err := reversePaymentTx(t.Context(), tx, event.PaymentID, reference, "provider", amount)
						if err != nil {
							return err
						}
						if result.Team != "a" || result.ReversedMicro < 0 || result.ReversedMicro > paid {
							return fmt.Errorf("invalid payer or reversal: %+v", result)
						}
						_, err = holdCreditFreezeTx(t.Context(), tx, result.Team, reference, event.PaymentID, "disputed", "", s.now())
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
	s, tenant, now := cardCollectionFixture(t)
	property := func(seed uint32) bool {
		first := now.Add(time.Duration(seed%1000) * time.Second)
		second := first.Add(time.Duration(seed%999999999 + 1))
		older := Card{Customer: "customer", PaymentMethod: "older", Fingerprint: "older-card", AddedAt: first}
		newer := Card{Customer: "customer", PaymentMethod: "newer", Fingerprint: "newer-card", AddedAt: second}
		for _, card := range []Card{older, newer, newer} {
			if err := tenant.SaveCard(t.Context(), card, "provider", second.Add(time.Hour)); err != nil {
				t.Log(err)
				return false
			}
		}
		if err := tenant.SaveCard(t.Context(), older, "provider", second.Add(2*time.Hour)); !errors.Is(err, ErrStaleCardSetup) {
			t.Logf("older completion accepted: %v", err)
			return false
		}
		standing, err := tenant.SpendStanding(t.Context(), second)
		if err != nil || standing.Card.PaymentMethod != newer.PaymentMethod || !standing.Card.AddedAt.Equal(second) {
			t.Logf("card or timestamp changed: %+v, %v", standing.Card, err)
			return false
		}
		unordered := newer
		unordered.AddedAt = time.Time{}
		if err := tenant.SaveCard(t.Context(), unordered, "provider", first); !errors.Is(err, ErrStaleCardSetup) {
			t.Logf("old receipt bypassed setup order: %v", err)
			return false
		}
		if err := tenant.SaveCard(t.Context(), unordered, "provider", second.Add(time.Second)); err != nil {
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
	standing, err := tenant.SpendStanding(t.Context(), now)
	if err != nil || standing.Card.PaymentMethod != "newer" || !standing.Card.AddedAt.Equal(now.Add(-time.Second)) {
		t.Fatalf("setup order did not survive reopening: %+v, %v", standing.Card, err)
	}
}

func TestPaymentMutationCollectionAmountsCannotOverflowProperty(t *testing.T) {
	s, _, now := cardCollectionFixture(t)
	cardCollectionDebt(t, s, "debt", 15000)
	w := cardCollectionDue(t, s, now)
	before := paymentMutationSnapshot(t, s)
	property := func(seed uint64) bool {
		minimum := int64(math.MaxInt64/MicroCreditsPerCent + 1)
		amount := minimum + int64(seed%uint64(math.MaxInt64-minimum))
		p := CardPayment{ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "overflow", AmountCents: amount}
		if created, warned, err := s.AsOperator().SettleCardPayment(t.Context(), p, now); err == nil || created || warned != nil {
			t.Logf("unrepresentable amount settled: %d, %t, %+v, %v", amount, created, warned, err)
			return false
		}
		return reflect.DeepEqual(paymentMutationSnapshot(t, s), before)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(42))}); err != nil {
		t.Fatal(err)
	}
}
