package store

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPaymentModeFixedByLedgerActivity(t *testing.T) {
	for _, activity := range []string{"grant", "charge", "checkout", "refill"} {
		t.Run(activity, func(t *testing.T) {
			s := authorityStore(t)
			if err := s.AsOperator().SetPaymentMode(t.Context(), capability.PaymentTest); err != nil {
				t.Fatal(err)
			}
			switch activity {
			case "grant":
				_, err := (&Tenant{s: s, team: "a"}).RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1, Reference: "first"})
				if err != nil {
					t.Fatal(err)
				}
			case "charge":
				err := s.inTx(t.Context(), func(tx *storeTx) error {
					l, err := lockLedgerTx(t.Context(), tx, "a")
					if err != nil {
						return err
					}
					return l.insertCharge(t.Context(), CreditCharge{ID: "first", Team: "a", Kind: CreditChargeStorage, AmountMicro: 1, ChargedAt: s.now()})
				})
				if err != nil {
					t.Fatal(err)
				}
			default:
				rewriteSchemaExec(t, s.db, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,kind,currency,amount_minor,amount_micro,opened_at,expires_at,closed_at,customer_id,payment_method) VALUES ('first','a','first','test',?,'usd',100,1000000,1,2,3,'customer','method')`, activity)
			}
			if err := s.AsOperator().SetPaymentMode(t.Context(), capability.PaymentLive); err == nil || !strings.Contains(err.Error(), "fresh store") {
				t.Fatalf("switch with %s: %v; want fresh-store error", activity, err)
			}
			if err := s.AsOperator().SetPaymentMode(t.Context(), capability.PaymentTest); err != nil {
				t.Fatalf("same mode: %v", err)
			}
		})
	}
}

func TestFreshStoreCanSwitchPaymentMode(t *testing.T) {
	s := authorityStore(t)
	for _, mode := range []capability.PaymentMode{capability.PaymentLive, capability.PaymentTest, capability.PaymentLive} {
		if err := s.AsOperator().SetPaymentMode(t.Context(), mode); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnconfiguredLedgerFixesPaymentMode(t *testing.T) {
	s := authorityStore(t)
	if _, err := (&Tenant{s: s, team: "a"}).RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1, Reference: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().SetPaymentMode(t.Context(), capability.PaymentLive); err == nil || !strings.Contains(err.Error(), "fresh store") {
		t.Fatalf("switch unconfigured ledger: %v", err)
	}
}

func TestFixedPaymentModeLedgerReports(t *testing.T) {
	for _, mode := range []capability.PaymentMode{capability.PaymentTest, capability.PaymentLive} {
		t.Run(string(mode), func(t *testing.T) {
			s := authorityStore(t)
			tenant := &Tenant{s: s, team: "a"}
			if err := s.AsOperator().SetPaymentMode(t.Context(), mode); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1000, Reference: "grant"}); err != nil {
				t.Fatal(err)
			}
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				l, err := lockLedgerTx(t.Context(), tx, "a")
				if err != nil {
					return err
				}
				return l.insertCharge(t.Context(), CreditCharge{Team: "a", ID: "charge", Kind: CreditChargeStorage, AmountMicro: 100, ChargedAt: s.now()})
			}); err != nil {
				t.Fatal(err)
			}
			state, err := tenant.CreditState(t.Context(), time.Hour)
			if err != nil || state.BalanceMicro != 900 || state.StorageChargedMicro != 100 || state.BurnMicro != 100 {
				t.Fatalf("state=%+v: %v", state, err)
			}
			grants, err := tenant.ListCreditGrants(t.Context(), 10)
			if err != nil || len(grants) != 1 {
				t.Fatalf("grants=%+v: %v", grants, err)
			}
			charges, err := tenant.ListCreditCharges(t.Context(), 10)
			if err != nil || len(charges) != 1 || charges[0].AmountMicro != 100 {
				t.Fatalf("charges=%+v: %v", charges, err)
			}
			totals, err := s.AsOperator().CreditLedgerTotals(t.Context())
			if err != nil || totals.BalanceMicro != 900 {
				t.Fatalf("totals=%+v: %v", totals, err)
			}
			id, err := tenant.HoldByOperator(t.Context(), "operator", "review")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.ReleaseCreditFreezes(t.Context(), id, "operator", "resolved"); err != nil {
				t.Fatal(err)
			}
			standing, err := tenant.BillingStanding(t.Context())
			if err != nil || standing.History.LastIncidentAt.IsZero() {
				t.Fatalf("standing=%+v: %v", standing, err)
			}
		})
	}
}
