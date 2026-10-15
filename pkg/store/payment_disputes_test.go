package store

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestFrozenTeamsMeteredClaimsAreRefused(t *testing.T) {
	for _, metered := range []bool{false, true} {
		t.Run(fmt.Sprintf("metered=%t", metered), func(t *testing.T) {
			s, demand, now := meteredUnitFixture(t, false)
			s.billing = capability.SettleBilling
			if !metered {
				root := authorityRoot("unmetered-runner", "a")
				root.Grants = []capability.Grant{capability.NodesClaim}
				authorityPersist(t, s, root)
				demand.CapabilityPrefix = root.Prefix
			}
			tenant := &Tenant{s: s, team: "a"}
			if _, err := tenant.TestOnlyHoldForDispute(t.Context(), "dispute", "payment", "opened", *now); err != nil {
				t.Fatal(err)
			}
			claim, err := s.ClaimUnit(t.Context(), demand)
			if metered {
				var refused *InsufficientCreditsError
				if !errors.As(err, &refused) || !refused.Frozen {
					t.Fatalf("frozen metered award=%+v: %v", claim, err)
				}
				rewriteSchemaCount(t, s.db, "billing_awards", 0)
				rewriteSchemaCount(t, s.db, "credit_charges", 0)
			} else if err != nil || claim.Award.CapabilityPrefix == "" {
				t.Fatalf("frozen unmetered award=%+v: %v", claim, err)
			}
			if count, err := tenant.ReleaseCreditFreezes(t.Context(), "dispute", "console", "resolved", *now); err != nil || count != 1 {
				t.Fatalf("release=%d: %v", count, err)
			}
			if metered {
				meteredUnitClaim(t, s, demand)
			}
		})
	}
}

func TestADisputeIsBoundToOnePayment(t *testing.T) {
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
		owner, stranger := &Tenant{s: s, team: "a"}, &Tenant{s: s, team: "b"}
		payment, foreignPayment := event.PaymentID, other.PaymentID
		if seed&1 == 1 {
			owner, stranger, payment, foreignPayment = stranger, owner, foreignPayment, payment
		}
		dispute := fmt.Sprintf("dispute-%d-%x", index, seed)
		now := s.now()
		created, err := owner.TestOnlyHoldForDispute(t.Context(), dispute, payment, "opened", now)
		if err != nil || !created {
			t.Fatalf("hold=%t: %v", created, err)
		}
		if _, err := stranger.TestOnlyHoldForDispute(t.Context(), dispute, foreignPayment, "forged", now); !errors.Is(err, ErrDisputeConflict) {
			return false
		}
		if _, err := s.AsOperator().TestOnlyReversePayment(t.Context(), foreignPayment, dispute, "console", 0); !errors.Is(err, ErrDisputeConflict) {
			return false
		}
		if count, err := stranger.ReleaseCreditFreezes(t.Context(), dispute, "console", "forged", now); err != nil || count != 0 {
			return false
		}
		if count, err := stranger.ReleaseOperatorHolds(t.Context(), "console", "forged", now); err != nil || count != 0 {
			return false
		}
		freeze, err := owner.CreditFreeze(t.Context())
		if err != nil || !freeze.Frozen || len(freeze.Disputes) != 1 || freeze.Disputes[0] != dispute {
			return false
		}
		if count, err := owner.ReleaseCreditFreezes(t.Context(), dispute, "console", "resolved", now); err != nil || count != 1 {
			return false
		}
		if replay, err := owner.TestOnlyHoldForDispute(t.Context(), dispute, payment, "replayed", now); err != nil || replay {
			return false
		}
		if _, err := stranger.TestOnlyHoldForDispute(t.Context(), dispute, foreignPayment, "replayed", now); !errors.Is(err, ErrDisputeConflict) {
			return false
		}
		for _, tenant := range []*Tenant{owner, stranger} {
			freeze, err := tenant.CreditFreeze(t.Context())
			balance, balanceErr := tenant.CreditBalanceMicro(t.Context())
			if err != nil || freeze.Frozen || balanceErr != nil || balance != 1000000 {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96, Rand: rand.New(rand.NewSource(5319))}); err != nil {
		t.Fatal(err)
	}
}

func TestAReversalMayNotUseTheUnappliedPrefix(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().TestOnlyReversePayment(t.Context(), event.PaymentID, "unapplied:refund", "console", 300000); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reserved reference=%v", err)
	}
	tenant := &Tenant{s: s, team: "a"}
	if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantReversal, AmountMicro: -300000, Reference: "unapplied:refund", Reverses: "payment:test:checkout", CreatedBy: "console"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reserved grant reference=%v", err)
	}
	if _, err := s.AsOperator().TestOnlyReversePayment(t.Context(), event.PaymentID, "refund", "console", 200000); err != nil {
		t.Fatal(err)
	}
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 800000 {
		t.Fatalf("balance=%d: %v", balance, err)
	}
}
