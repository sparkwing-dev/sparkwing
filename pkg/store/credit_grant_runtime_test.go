package store

import (
	"errors"
	"math"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestGrantCreditsIsIdempotentByReference(t *testing.T) {
	s := authorityStore(t)
	for _, team := range []Team{"a", "b"} {
		tenant := &Tenant{s: s, team: team}
		request := CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 50000, Reference: "welcome", CreatedBy: "ops"}
		first, err := tenant.RecordCreditGrant(t.Context(), request)
		if err != nil || !first.Created {
			t.Fatalf("first free grant: %+v, %v", first, err)
		}
		again, err := tenant.RecordCreditGrant(t.Context(), request)
		if err != nil || again.Created || again.Grant.ID != first.Grant.ID {
			t.Fatalf("free grant replay: %+v, %v", again, err)
		}
		request.AmountMicro--
		if _, err := tenant.RecordCreditGrant(t.Context(), request); !errors.Is(err, ErrCreditGrantConflict) {
			t.Fatalf("changed grant terms: %v", err)
		}
		if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 50000 {
			t.Fatalf("%s balance: %d, %v", team, balance, err)
		}
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 2)
}

func TestGrantCreditsRepeatsAnEmptyReference(t *testing.T) {
	s := authorityStore(t)
	tenant := &Tenant{s: s, team: "a"}
	for range 2 {
		if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 50000, Reference: "", CreatedBy: "ops"}); err != nil {
			t.Fatal(err)
		}
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 2)
}

func TestCreditsBalanceIsGrantsMinusCharges(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 50000, Reference: "payment:test:checkout", CreatedBy: "ops"}); err != nil {
		t.Fatal(err)
	}
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 1050000 {
		t.Fatalf("separate free and paid references: %d, %v", balance, err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES ('spent','a','run','job','','usage',1,500000,1)`)
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 550000 {
		t.Fatalf("grants minus charges: %d, %v", balance, err)
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 2)
}

func TestCreditGrantRequestValidation(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	for _, req := range []CreditGrantRequest{
		{Kind: "gift", AmountMicro: 1},
		{Kind: CreditGrantFree},
		{Kind: CreditGrantFree, AmountMicro: -1},
		{Kind: CreditGrantFree, AmountMicro: MaxCreditGrantMicro + 1},
		{Kind: CreditGrantFree, AmountMicro: 1, Reference: unappliedPrefix + "reserved"},
		{Kind: CreditGrantFree, AmountMicro: 1, Reverses: "payment:test:checkout"},
		{Kind: CreditGrantPaid, AmountMicro: 1},
		{Kind: CreditGrantReversal, AmountMicro: 1, Reference: "refund", Reverses: "payment:test:checkout"},
		{Kind: CreditGrantReversal, AmountMicro: -1, Reverses: "payment:test:checkout"},
		{Kind: CreditGrantReversal, AmountMicro: -1, Reference: "refund"},
		{Kind: CreditGrantReversal, AmountMicro: math.MinInt64, Reference: "refund", Reverses: "payment:test:checkout"},
	} {
		if _, err := tenant.RecordCreditGrant(t.Context(), req); err == nil {
			t.Fatalf("invalid grant accepted: %+v", req)
		}
	}
	if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: MaxCreditGrantMicro, Reference: "gift", CreatedBy: "ops"}); !errors.Is(err, ErrCreditBalanceCap) {
		t.Fatalf("free grant ceiling bypassed balance cap: %v", err)
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 1)
}

func TestCreditReversalNetsOutOfTheBalanceAndTheHistory(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	request := CreditGrantRequest{Kind: CreditGrantReversal, AmountMicro: -400000, Reference: "refund", Reverses: "payment:test:checkout", CreatedBy: "ops"}
	first, err := tenant.RecordCreditGrant(t.Context(), request)
	if err != nil || !first.Created || first.Grant.Reverses != request.Reverses {
		t.Fatalf("partial reversal: %+v, %v", first, err)
	}
	again, err := tenant.RecordCreditGrant(t.Context(), request)
	if err != nil || again.Created || again.Grant.ID != first.Grant.ID {
		t.Fatalf("reversal replay: %+v, %v", again, err)
	}
	request.AmountMicro--
	if _, err := tenant.RecordCreditGrant(t.Context(), request); !errors.Is(err, ErrCreditGrantConflict) {
		t.Fatalf("changed reversal: %v", err)
	}
	state, err := tenant.CreditState(t.Context(), 0)
	if err != nil || state.GrantedMicro != 1000000 || state.ReversedMicro != 400000 || state.BalanceMicro != 600000 {
		t.Fatalf("reversal state: %+v, %v", state, err)
	}
	totals, err := s.AsOperator().CreditLedgerTotals(t.Context())
	if err != nil || totals.ReversedMicro != 400000 || totals.BalanceMicro != 600000 {
		t.Fatalf("reversal totals: %+v, %v", totals, err)
	}
	grants, err := tenant.ListCreditGrants(t.Context(), 10)
	if err != nil || len(grants) != 2 {
		t.Fatalf("reversal history: %+v, %v", grants, err)
	}
	for _, grant := range grants {
		if grant.Kind == CreditGrantReversal && (grant.AmountMicro != -400000 || grant.Reverses != "payment:test:checkout") {
			t.Fatalf("reversal history lost identity: %+v", grant)
		}
	}
}

func TestCreditReversalRefusesUnfundedOrExcessiveMovements(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	for _, req := range []CreditGrantRequest{
		{Kind: CreditGrantReversal, AmountMicro: -1, Reference: "refund", Reverses: "missing"},
		{Kind: CreditGrantReversal, AmountMicro: -1000001, Reference: "refund", Reverses: "payment:test:checkout"},
	} {
		if _, err := tenant.RecordCreditGrant(t.Context(), req); err == nil {
			t.Fatalf("unfunded reversal accepted: %+v", req)
		}
	}
	foreign := &Tenant{s: s, team: "b"}
	request := CreditGrantRequest{Kind: CreditGrantReversal, AmountMicro: -1000000, Reference: "refund", Reverses: "payment:test:checkout"}
	if _, err := foreign.RecordCreditGrant(t.Context(), request); err == nil {
		t.Fatal("foreign team reversed a checkout")
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES ('spent','a','retained-run','job','','usage',1,600000,1)`)
	if _, err := tenant.RecordCreditGrant(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != -600000 {
		t.Fatalf("spent credit reversed: %d, %v", balance, err)
	}
}
