package controller_test

import (
	"net/http"
	"testing"
)

type cardBilling struct {
	BalanceMicro int64 `json:"balance_micro"`
	CanPurchase  bool  `json:"can_purchase"`
	CanAddCard   bool  `json:"can_add_card"`
	CardBilled   bool  `json:"card_billed"`
	Card         *struct {
		Last4 string `json:"last4"`
	} `json:"card"`
	OpenCharge *struct {
		ID          string `json:"id"`
		AmountCents int64  `json:"amount_cents"`
		Failures    int64  `json:"failures"`
		DeclineCode string `json:"decline_code"`
	} `json:"open_charge"`
	Limits struct {
		BudgetCents      int64  `json:"budget_cents"`
		CreditLimitCents int64  `json:"credit_limit_cents"`
		Binding          string `json:"binding"`
	} `json:"limits"`
}

func (f *identityFixture) billing(who signedIn) cardBilling {
	f.t.Helper()
	var b cardBilling
	if code := f.call("GET", "/api/v1/team/billing", who.auth, nil, &b); code != http.StatusOK {
		f.t.Fatalf("billing = %d", code)
	}
	return b
}

func TestCardBilling_OwnerSetsABudget(t *testing.T) {
	f, _ := billingFixture(t)
	owner, editor, _ := teamOf(f)
	if code := f.call("PUT", "/api/v1/team/billing/budget", editor.auth, map[string]any{"budget_cents": 1_000}, nil); code != http.StatusForbidden {
		t.Fatalf("an editor's budget = %d, want 403", code)
	}
	if code := f.call("PUT", "/api/v1/team/billing/budget", owner.auth, map[string]any{"budget_cents": 1_000}, nil); code != http.StatusNoContent {
		t.Fatalf("budget = %d", code)
	}
	if b := f.billing(owner); b.Limits.BudgetCents != 1_000 {
		t.Fatalf("budget reads %d", b.Limits.BudgetCents)
	}
	if code := f.call("PUT", "/api/v1/team/billing/budget", owner.auth, map[string]any{"budget_cents": 100_000_000}, nil); code != http.StatusBadRequest {
		t.Fatalf("an oversized budget = %d, want 400", code)
	}
}
