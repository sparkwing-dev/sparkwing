package store

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPaymentWarningCannotChangePaymentOrCardProperty(t *testing.T) {
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
		warning := PaymentWarning{WarningID: fmt.Sprintf("warning-%d-%x", index, seed), PaymentIntent: event.PaymentID, Fingerprint: fmt.Sprintf("card-%x", seed), Actionable: true}
		owner, stranger := Team("a"), Team("b")
		if seed&1 != 0 {
			warning.PaymentIntent, owner, stranger = other.PaymentID, stranger, owner
		}
		now := time.Unix(1000, int64(index)).UTC()
		if team, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), warning, now); err != nil || team != owner {
			t.Logf("first warning: %q, %v", team, err)
			return false
		}
		for _, mutate := range []func(*PaymentWarning){
			func(w *PaymentWarning) {
				w.PaymentIntent = other.PaymentID
				if owner == "b" {
					w.PaymentIntent = event.PaymentID
				}
			},
			func(w *PaymentWarning) { w.Fingerprint += "-other" },
		} {
			changed := warning
			mutate(&changed)
			if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), changed, now); !errors.Is(err, ErrCreditGrantConflict) {
				t.Logf("changed warning accepted: %+v, %v", changed, err)
				return false
			}
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
	if err := quick.Check(property, &quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(42))}); err != nil {
		t.Fatal(err)
	}
}

func TestPaymentWarningRefusesAmbiguousPayerProperty(t *testing.T) {
	for _, paid := range []bool{false, true} {
		t.Run(fmt.Sprintf("paid=%t", paid), func(t *testing.T) {
			s, event := paymentFixture(t, capability.PaymentTest)
			if paid {
				if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			index := 0
			property := func(seed uint64) bool {
				index++
				payment := fmt.Sprintf("payment-%d-%x", index, seed)
				if paid {
					payment = event.PaymentID
				} else {
					rewriteSchemaExec(t, s.db, `INSERT INTO card_attempts(id,team,charge_id,kind,status,payment_intent,created_at,updated_at) VALUES (?,'a',?,'off_session','live',?,1,1)`, payment+"-a", payment, payment)
				}
				rewriteSchemaExec(t, s.db, `INSERT INTO card_attempts(id,team,charge_id,kind,status,payment_intent,created_at,updated_at) VALUES (?,'b',?,'off_session','live',?,1,1)`, fmt.Sprintf("attempt-%d", index), fmt.Sprintf("charge-%d", index), payment)
				warning := PaymentWarning{WarningID: fmt.Sprintf("warning-%d", index), PaymentIntent: payment, Actionable: true}
				if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), warning, s.now()); !errors.Is(err, ErrCreditGrantConflict) {
					t.Logf("ambiguous payer accepted: %v", err)
					return false
				}
				rewriteSchemaCount(t, s.db, "payment_warnings", 0)
				rewriteSchemaCount(t, s.db, "credit_freezes", 0)
				return true
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 32, Rand: rand.New(rand.NewSource(42))}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPaymentWarningHoldAndAuditRollbackAndReplayAfterRestart(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	path := blobAccountingDiskStore(t, s)
	warning := PaymentWarning{WarningID: "warning", PaymentIntent: event.PaymentID, Fingerprint: "card", Actionable: true}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_warning_audit BEFORE INSERT ON business_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
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
	if count, err := tenant.ReleaseCreditFreezes(t.Context(), hold, "console", "reviewed", s.now()); err != nil || count != 1 {
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
	warning := PaymentWarning{WarningID: "warning", PaymentIntent: event.PaymentID, Actionable: true}
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
		if count, err := tenant.ReleaseCreditFreezes(t.Context(), id, "console", "reviewed", s.now()); err != nil || count != 1 {
			t.Fatalf("warning release: %d, %v", count, err)
		}
	}
	freeze, err = tenant.CreditFreeze(t.Context())
	if err != nil || !freeze.Frozen || len(freeze.Disputes) != 1 || freeze.Disputes[0] != warning.WarningID {
		t.Fatalf("warning release changed the unrelated dispute: %+v, %v", freeze, err)
	}
}

func TestSharedFraudWarningHoldsEveryTeam(t *testing.T) {
	for _, order := range []string{"warning-first", "payment-first", "concurrent-payments"} {
		t.Run(order, func(t *testing.T) {
			s, first, now := cardCollectionFixture(t)
			second := &Tenant{s: s, team: "b"}
			if _, _, err := second.SetBillingTrust(t.Context(), BillingTrustChange{Trust: BillingTrustGranted, Actor: "console", Reason: "customer"}, now); err != nil {
				t.Fatal(err)
			}
			if err := second.TestOnlySaveCard(t.Context(), Card{Customer: "customer-b", PaymentMethod: "method-b", Fingerprint: "fp_1"}, "billing", now); err != nil {
				t.Fatal(err)
			}
			for _, tenant := range []*Tenant{first, second} {
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES (?,?,'','','','usage',0,?,?)`, "debt-"+string(tenant.team), tenant.team, 10000*MicroCreditsPerCent, now.UnixNano())
			}
			work, _, err := s.AsOperator().DueCardCharges(t.Context(), now)
			if err != nil || len(work) != 2 {
				t.Fatalf("charges: %+v, %v", work, err)
			}
			payments := map[Team]CardPayment{}
			for _, w := range work {
				payments[w.Team] = CardPayment{ChargeID: w.ChargeID, AttemptID: w.AttemptID, PaymentIntent: "payment-" + string(w.Team), AmountCents: w.AmountCents, Fingerprint: "fp_1"}
			}
			if order == "payment-first" {
				if created, _, err := s.AsOperator().SettleCardPayment(t.Context(), payments[first.team], now); err != nil || !created {
					t.Fatalf("first payment: %t, %v", created, err)
				}
			}
			warning := PaymentWarning{WarningID: "shared-warning", PaymentIntent: payments[first.team].PaymentIntent, Fingerprint: "fp_1", Actionable: true}
			if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), warning, now); err != nil {
				t.Fatal(err)
			}
			settle := func(team Team) error {
				if order == "payment-first" && team == first.team {
					return nil
				}
				created, warned, err := s.AsOperator().SettleCardPayment(t.Context(), payments[team], now)
				if err != nil {
					return err
				}
				if created || (warned != nil && warned.WarningID != warning.WarningID) {
					return fmt.Errorf("warned payment granted or warning lost: %s, %t, %+v", team, created, warned)
				}
				return nil
			}
			results := make(chan error, 2)
			for _, team := range []Team{first.team, second.team} {
				if order == "concurrent-payments" {
					go func() { results <- settle(team) }()
				} else {
					results <- settle(team)
				}
			}
			for range 2 {
				if err := <-results; err != nil {
					t.Fatal(err)
				}
			}
			ids := map[Team]string{}
			for _, tenant := range []*Tenant{first, second} {
				freeze, err := tenant.CreditFreeze(t.Context())
				if err != nil || !freeze.Frozen || len(freeze.Disputes) != 1 {
					t.Fatalf("%s hold: %+v, %v", tenant.team, freeze, err)
				}
				ids[tenant.team] = freeze.Disputes[0]
				standing, err := tenant.SpendStanding(t.Context(), now)
				if err != nil || standing.Billing.Trusted || standing.CreditLimitMicro != 0 {
					t.Fatalf("%s standing: %+v, %v", tenant.team, standing, err)
				}
				want := int64(-10000 * MicroCreditsPerCent)
				if order == "payment-first" && tenant == first {
					want = 0
				}
				if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != want {
					t.Fatalf("%s balance: %d, want %d, %v", tenant.team, balance, want, err)
				}
				if err := settle(tenant.team); err != nil {
					t.Fatal(err)
				}
			}
			if ids[first.team] == ids[second.team] {
				t.Fatal("teams share a warning hold identity")
			}
			if order == "payment-first" {
				for range 2 {
					if _, err := s.AsOperator().TestOnlyReversePayment(t.Context(), payments[first.team].PaymentIntent, "refund", "console", 10000*MicroCreditsPerCent); err != nil {
						t.Fatal(err)
					}
				}
				if balance, err := first.CreditBalanceMicro(t.Context()); err != nil || balance != -10000*MicroCreditsPerCent {
					t.Fatalf("refund replay: %d, %v", balance, err)
				}
			}
			if count, err := first.ReleaseCreditFreezes(t.Context(), ids[first.team], "console", "reviewed", now); err != nil || count != 1 {
				t.Fatalf("first release: %d, %v", count, err)
			}
			if _, err := s.AsOperator().TestOnlyRecordPaymentWarning(t.Context(), warning, now); err != nil {
				t.Fatal(err)
			}
			if err := settle(first.team); err != nil {
				t.Fatal(err)
			}
			for _, tenant := range []*Tenant{first, second} {
				freeze, err := tenant.CreditFreeze(t.Context())
				if err != nil || freeze.Frozen != (tenant == second) {
					t.Fatalf("%s released hold replay: %+v, %v", tenant.team, freeze, err)
				}
			}
			refunds, err := s.AsOperator().DueCardRefunds(t.Context(), now)
			if err != nil || len(refunds) != 0 {
				t.Fatalf("warning created refund work: %+v, %v", refunds, err)
			}
		})
	}
}
