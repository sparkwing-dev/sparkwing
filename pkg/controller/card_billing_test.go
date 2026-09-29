package controller_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

func (f *identityFixture) owe(team string, cents int64) {
	f.t.Helper()
	if _, err := f.store.DB().Exec(fmt.Sprintf(
		`INSERT INTO credit_charges (id, run_id, node_id, token_prefix, kind, seconds, amount_micro, charged_at, team)
		 VALUES ('ch_%d', 'run-x', 'build', 'pfx', 'usage', 60, %d, %d, '%s')`,
		time.Now().UnixNano(), cents*store.MicroCreditsPerCent, time.Now().UnixNano(), team)); err != nil {
		f.t.Fatalf("seed a debt: %v", err)
	}
}

func (f *identityFixture) billing(who signedIn) cardBilling {
	f.t.Helper()
	var b cardBilling
	if code := f.call("GET", "/api/v1/team/billing", who.auth, nil, &b); code != http.StatusOK {
		f.t.Fatalf("billing = %d", code)
	}
	return b
}

// Only a trusted team's owner adds a card; once one is on file the team
// buys no prepaid credit and its debt is charged to the card.
func TestCardBilling_ATrustedOwnerAddsACardAndIsCharged(t *testing.T) {
	f, fc := billingFixture(t)
	owner, editor, _ := teamOf(f)

	if code := f.call("POST", "/api/v1/team/billing/card", owner.auth, nil, nil); code != http.StatusConflict {
		t.Fatalf("a New team adding a card = %d, want 409", code)
	}
	f.trust(owner.team, 0)
	if code := f.call("POST", "/api/v1/team/billing/card", editor.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("an editor adding a card = %d, want 403", code)
	}
	var page struct {
		URL string `json:"url"`
	}
	if code := f.call("POST", "/api/v1/team/billing/card", owner.auth, nil, &page); code != http.StatusOK ||
		page.URL == "" {
		t.Fatalf("owner adding a card = %d %+v", code, page)
	}
	setups := fc.internalCalls("/internal/card-setup")
	if len(setups) != 1 || setups[0].Body["team"] != owner.team || setups[0].Body["email"] != "olga@example.com" {
		t.Fatalf("card setups = %+v", setups)
	}

	card := map[string]any{
		"team": owner.team, "customer": "cus_1", "payment_method": "pm_1",
		"fingerprint": "fp_1", "brand": "visa", "last4": "4242",
	}
	if code := f.call("POST", "/api/v1/credits/cards", owner.auth, card, nil); code != http.StatusForbidden {
		t.Fatalf("an owner saving a card directly = %d, want 403", code)
	}
	if code := f.call("POST", "/api/v1/credits/cards", "Bearer "+f.admin, card, nil); code != http.StatusNoContent {
		t.Fatalf("the service saving the card = %d", code)
	}
	b := f.billing(owner)
	if b.Card == nil || b.Card.Last4 != "4242" || !b.CardBilled || b.CanPurchase || b.Limits.CreditLimitCents != 20_000 {
		t.Fatalf("billing with a card = %+v", b)
	}
	if code := f.call("POST", "/api/v1/team/billing/checkout", owner.auth,
		map[string]any{"amount_cents": 2_500}, nil); code != http.StatusConflict || len(fc.calls()) != 0 {
		t.Fatalf("a carded team buying prepaid = %d, calls %d; want 409 and none", code, len(fc.calls()))
	}

	f.owe(owner.team, 15_000)
	f.srv.CardBillingPass(context.Background())
	charges := fc.internalCalls("/internal/charge")
	if len(charges) != 1 || charges[0].Body["amount_cents"] != float64(15_000) ||
		charges[0].Body["payment_method"] != "pm_1" || charges[0].Body["customer"] != "cus_1" {
		t.Fatalf("charges = %+v, want $150 to pm_1", charges)
	}
	if b := f.billing(owner); b.BalanceMicro != 0 || b.OpenCharge != nil {
		t.Fatalf("after the charge = %+v, want a whole balance and no open charge", b)
	}
	f.srv.CardBillingPass(context.Background())
	if n := len(fc.internalCalls("/internal/charge")); n != 1 {
		t.Fatalf("a settled debt was charged again: %d charges", n)
	}
	if code := f.call("POST", "/api/v1/teams/"+owner.team+"/trust", "Bearer "+f.admin,
		map[string]any{"trust": "revoked", "reason": "test"}, nil); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	// safety: the $150 card payment counts toward the New team's 30-day
	// purchase limit, so the refusal is that limit and not the card.
	var refused struct {
		Code string `json:"code"`
	}
	if code := f.call("POST", "/api/v1/team/billing/checkout", owner.auth,
		map[string]any{"amount_cents": 2_500}, &refused); code != http.StatusConflict || refused.Code != "purchase_limit" {
		t.Fatalf("a revoked team with a card buying prepaid = %d %q, want the purchase limit", code, refused.Code)
	}
}

// A declined charge stops new work and waits for its retry; "pay now" opens
// a Checkout for the same charge, and its payment releases the team.
func TestCardBilling_ADeclineIsPaidByHand(t *testing.T) {
	f, fc := billingFixture(t)
	owner, _, reader := teamOf(f)
	f.trust(owner.team, 0)
	if code := f.call("POST", "/api/v1/credits/cards", "Bearer "+f.admin, map[string]any{
		"team":     owner.team,
		"customer": "cus_1", "payment_method": "pm_1", "fingerprint": "fp_1", "last4": "4242",
	}, nil); code != http.StatusNoContent {
		t.Fatalf("save card = %d", code)
	}
	if code := f.call("POST", "/api/v1/team/billing/pay", owner.auth, nil, nil); code != http.StatusConflict {
		t.Fatalf("paying with nothing owed = %d, want 409", code)
	}
	fc.mu.Lock()
	fc.chargeStatus, fc.declineCode = "failed", "insufficient_funds"
	fc.mu.Unlock()
	f.owe(owner.team, 15_000)
	f.srv.CardBillingPass(context.Background())

	b := f.billing(owner)
	if b.OpenCharge == nil || b.OpenCharge.Failures != 1 || b.OpenCharge.DeclineCode != "insufficient_funds" ||
		b.Limits.CreditLimitCents != 0 || b.Limits.Binding != store.SpendLimitChargeFailed {
		t.Fatalf("after a decline = %+v", b)
	}
	f.srv.CardBillingPass(context.Background())
	if n := len(fc.internalCalls("/internal/charge")); n != 1 {
		t.Fatalf("a declined charge was retried at once: %d charges", n)
	}

	if code := f.call("POST", "/api/v1/team/billing/pay", reader.auth, nil, nil); code != http.StatusForbidden {
		t.Fatalf("a reader paying = %d, want 403", code)
	}
	if code := f.call("POST", "/api/v1/team/billing/pay", owner.auth, nil, nil); code != http.StatusOK {
		t.Fatalf("pay now = %d", code)
	}
	pays := fc.internalCalls("/internal/charge-checkout")
	if len(pays) != 1 || pays[0].Body["charge_id"] != b.OpenCharge.ID || pays[0].Body["amount_cents"] != float64(15_000) {
		t.Fatalf("pay-now checkouts = %+v", pays)
	}
	paid := map[string]any{
		"team": owner.team, "charge_id": b.OpenCharge.ID, "attempt_id": pays[0].Body["attempt_id"],
		"payment_intent": "pi_recovered", "amount_cents": 15_000, "status": "succeeded",
	}
	if code := f.call("POST", "/api/v1/credits/card-payments", owner.auth, paid, nil); code != http.StatusForbidden {
		t.Fatalf("an owner reporting a payment = %d, want 403", code)
	}
	if code := f.call("POST", "/api/v1/credits/card-payments", "Bearer "+f.admin, paid, nil); code != http.StatusNoContent {
		t.Fatalf("the webhook's payment = %d", code)
	}
	if b := f.billing(owner); b.OpenCharge != nil || b.BalanceMicro != 0 || b.Limits.CreditLimitCents == 0 {
		t.Fatalf("after paying = %+v", b)
	}
}

// An owner lowers the budget; a budget above the largest limit is refused.
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
