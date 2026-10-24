package store

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"

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

func TestFreeGrantCannotCreatePaymentMovementsProperty(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	before := paymentMutationSnapshot(t, s)
	property := func(seed uint32, kind uint8) bool {
		request := CreditGrantRequest{Kind: creditGrantReversal, AmountMicro: -int64(seed%999999 + 1), Reference: fmt.Sprintf("refund-%x", seed), Reverses: "payment:test:checkout", CreatedBy: "operator"}
		switch kind % 3 {
		case 1:
			request.Kind, request.AmountMicro = CreditGrantPaid, -request.AmountMicro
		case 2:
			request.Kind, request.AmountMicro = CreditGrantFree, -request.AmountMicro
		}
		got, err := tenant.RecordCreditGrant(t.Context(), request)
		if err == nil || got.Created || got.Grant.ID != "" {
			t.Logf("grant accepted payment movement: %+v, %+v, %v", request, got, err)
			return false
		}
		return reflect.DeepEqual(before, paymentMutationSnapshot(t, s))
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48, Rand: rand.New(rand.NewSource(41))}); err != nil {
		t.Fatal(err)
	}
}
