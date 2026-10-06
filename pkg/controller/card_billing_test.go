package controller_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
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

// A pay-now payment by a card that drew a fraud warning on another payment
// repays the debt, and the operator is alerted once.
func TestCardBilling_AWarnedCardRepaysTheDebtAndAlerts(t *testing.T) {
	fc, url := newFakeCheckout(t)
	raw, pub := multiTeamLicense(t)
	logs := &lockedBuffer{}
	f := newIdentityFixtureWith(t, fixtureOpts{
		license: raw, key: pub, checkoutURL: url, checkoutToken: checkoutToken,
		logger: slog.New(slog.NewJSONHandler(logs, nil)),
	})
	owner, _, _ := teamOf(f)
	f.trust(owner.team, 0)
	if code := f.call("POST", "/api/v1/credits/cards", "Bearer "+f.admin, map[string]any{
		"team": owner.team, "customer": "cus_1", "payment_method": "pm_1", "fingerprint": "fp_1", "last4": "4242",
	}, nil); code != http.StatusNoContent {
		t.Fatalf("save card = %d", code)
	}
	fc.mu.Lock()
	fc.chargeStatus, fc.declineCode = "failed", "insufficient_funds"
	fc.mu.Unlock()
	f.owe(owner.team, 15_000)
	f.srv.CardBillingPass(context.Background())
	if code := f.call("POST", "/api/v1/credits/warnings", "Bearer "+f.admin, map[string]any{
		"payment_intent": "pi_elsewhere", "warning_id": "issfr_1", "fingerprint": "fp_flagged", "actionable": true,
	}, nil); code >= 300 {
		t.Fatalf("warning = %d", code)
	}
	if code := f.call("POST", "/api/v1/team/billing/pay", owner.auth, nil, nil); code != http.StatusOK {
		t.Fatalf("pay now = %d", code)
	}
	pays := fc.internalCalls("/internal/charge-checkout")
	paid := map[string]any{
		"team": owner.team, "charge_id": pays[0].Body["charge_id"], "attempt_id": pays[0].Body["attempt_id"],
		"payment_intent": "pi_recovered", "amount_cents": 15_000, "status": "succeeded", "fingerprint": "fp_flagged",
	}
	for range 2 {
		if code := f.call("POST", "/api/v1/credits/card-payments", "Bearer "+f.admin, paid, nil); code != http.StatusNoContent {
			t.Fatalf("the webhook's payment = %d", code)
		}
	}
	if b := f.billing(owner); b.OpenCharge != nil || b.BalanceMicro != 0 {
		t.Fatalf("after paying = %+v, want the debt repaid", b)
	}
	alerts := logs.records(t, "billing alert: a card with a fraud warning repaid a debt; the team is held for review")
	if len(alerts) != 1 || alerts[0]["alert"] != "card_payment_warned" || alerts[0]["warning_id"] != "issfr_1" ||
		alerts[0]["payment_intent"] != "pi_recovered" {
		t.Fatalf("alerts = %v, want one card_payment_warned", alerts)
	}
}

// A saved card names when its setup completed, and a setup that completed
// before the card on file is refused with stale_card_setup and changes nothing.
func TestCardBilling_AnOlderSetupDoesNotReplaceANewerCard(t *testing.T) {
	f, _ := billingFixture(t)
	owner, _, _ := teamOf(f)
	f.trust(owner.team, 0)
	save := func(pm, last4 string, completed int64) (int, string) {
		var refused struct {
			Code string `json:"code"`
		}
		code := f.call("POST", "/api/v1/credits/cards", "Bearer "+f.admin, map[string]any{
			"team": owner.team, "customer": "cus_1", "payment_method": pm, "fingerprint": "fp_" + pm,
			"last4": last4, "completed_at": completed,
		}, &refused)
		return code, refused.Code
	}
	at := time.Now().Add(time.Hour).Unix()
	for _, c := range []struct {
		pm, last4 string
		completed int64
		code      int
		refusal   string
	}{
		{"pm_old", "0000", at, http.StatusNoContent, ""},
		{"pm_new", "1111", at + 60, http.StatusNoContent, ""},
		{"pm_old", "0000", at, http.StatusConflict, controller.StaleCardSetupCode},
	} {
		if code, refusal := save(c.pm, c.last4, c.completed); code != c.code || refusal != c.refusal {
			t.Fatalf("save %s at %d = %d %q, want %d %q", c.pm, c.completed, code, refusal, c.code, c.refusal)
		}
	}
	if b := f.billing(owner); b.Card == nil || b.Card.Last4 != "1111" {
		t.Fatalf("card = %+v, want the newer card kept", b.Card)
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

// The worker creates the payment, records its id, and confirms it by that
// id; a second payment for the same charge is refunded once; and the units
// route names the card-billing contract the checkout service requires.
func TestCardBilling_ChargesByRecordedIntentAndRefundsASecondPayment(t *testing.T) {
	f, fc := billingFixture(t)
	fc.twoStep = true
	owner, _, _ := teamOf(f)
	f.trust(owner.team, 0)
	if code := f.call("POST", "/api/v1/credits/cards", "Bearer "+f.admin, map[string]any{
		"team": owner.team, "customer": "cus_1", "payment_method": "pm_1", "fingerprint": "fp_1",
	}, nil); code != http.StatusNoContent {
		t.Fatalf("save card = %d", code)
	}
	f.owe(owner.team, 15_000)
	f.srv.CardBillingPass(context.Background())
	charges := fc.internalCalls("/internal/charge")
	if len(charges) != 2 || charges[0].Body["payment_intent"] != "" ||
		charges[1].Body["payment_intent"] != "pi_"+fmt.Sprint(charges[0].Body["attempt_id"]) {
		t.Fatalf("charges = %+v; want a create, then a confirm naming the created payment", charges)
	}
	if b := f.billing(owner); b.BalanceMicro != 0 || b.OpenCharge != nil {
		t.Fatalf("after the charge = %+v", b)
	}
	second := map[string]any{
		"team": owner.team, "charge_id": charges[0].Body["charge_id"], "attempt_id": charges[0].Body["attempt_id"],
		"payment_intent": "pi_second", "amount_cents": 15_000, "status": "succeeded",
	}
	if code := f.call("POST", "/api/v1/credits/card-payments", "Bearer "+f.admin, second, nil); code != http.StatusNoContent {
		t.Fatalf("second payment = %d", code)
	}
	f.srv.CardBillingPass(context.Background())
	f.srv.CardBillingPass(context.Background())
	refunds := fc.internalCalls("/internal/refund")
	if len(refunds) != 1 || refunds[0].Body["payment_intent"] != "pi_second" {
		t.Fatalf("refunds = %+v; want the second payment refunded once", refunds)
	}
	if err := f.store.AsOperator().CreateTeam(t.Context(), "other"); err != nil {
		t.Fatal(err)
	}
	other, err := f.store.ForTeam(t.Context(), "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.RecordCreditGrant(t.Context(), store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: 15_000 * store.MicroCreditsPerCent, Reference: "pi_other_team",
	}); err != nil {
		t.Fatal(err)
	}
	second["payment_intent"] = "pi_other_team"
	if code := f.call("POST", "/api/v1/credits/card-payments", "Bearer "+f.admin, second, nil); code != http.StatusConflict {
		t.Fatalf("another team's payment reference = %d, want 409", code)
	}
	var units struct {
		Capabilities []string `json:"capabilities"`
	}
	if code := f.call("GET", "/api/v1/credits/units", "Bearer "+f.admin, nil, &units); code != http.StatusOK ||
		strings.Join(units.Capabilities, ",") != "card-billing-v1,paid-grant-lookup-v1,card-setup-order-v1" {
		t.Fatalf("units = %d %+v", code, units)
	}
}

// The worker settles with the card that paid, so a charge on a card that drew
// an actionable warning is not granted; a queued refund's failure reported by
// the checkout service puts it back in the queue.
func TestCardBilling_JudgesThePayingCardAndRequeuesAFailedRefund(t *testing.T) {
	f, fc := billingFixture(t)
	fc.twoStep, fc.fingerprint = true, "fp_warned"
	owner, _, _ := teamOf(f)
	f.trust(owner.team, 0)
	if code := f.call("POST", "/api/v1/credits/cards", "Bearer "+f.admin, map[string]any{
		"team": owner.team, "customer": "cus_1", "payment_method": "pm_1", "fingerprint": "fp_1",
	}, nil); code != http.StatusNoContent {
		t.Fatalf("save card = %d", code)
	}
	var warned struct {
		Held bool `json:"held"`
	}
	if code := f.call("POST", "/api/v1/credits/warnings", "Bearer "+f.admin, map[string]any{
		"payment_intent": "pi_elsewhere", "warning_id": "issfr_1", "fingerprint": "fp_warned", "actionable": true,
	}, &warned); code != http.StatusOK || warned.Held {
		t.Fatalf("warning = %d %+v", code, warned)
	}
	f.owe(owner.team, 15_000)
	f.srv.CardBillingPass(context.Background())
	if b := f.billing(owner); b.BalanceMicro >= 0 || b.OpenCharge == nil {
		t.Fatalf("after a charge on the warned card = %+v; want nothing granted", b)
	}

	g, gc := billingFixture(t)
	gc.twoStep = true
	other, _, _ := teamOf(g)
	g.trust(other.team, 0)
	if code := g.call("POST", "/api/v1/credits/cards", "Bearer "+g.admin, map[string]any{
		"team": other.team, "customer": "cus_2", "payment_method": "pm_2", "fingerprint": "fp_2",
	}, nil); code != http.StatusNoContent {
		t.Fatalf("save card = %d", code)
	}
	g.owe(other.team, 15_000)
	g.srv.CardBillingPass(context.Background())
	charges := gc.internalCalls("/internal/charge")
	if code := g.call("POST", "/api/v1/credits/card-payments", "Bearer "+g.admin, map[string]any{
		"team": other.team, "charge_id": charges[0].Body["charge_id"], "attempt_id": charges[0].Body["attempt_id"],
		"payment_intent": "pi_second", "amount_cents": 15_000, "status": "succeeded",
	}, nil); code != http.StatusNoContent {
		t.Fatalf("second payment = %d", code)
	}
	g.srv.CardBillingPass(context.Background())
	var report struct {
		Queued bool `json:"queued"`
	}
	if code := g.call("POST", "/api/v1/credits/card-refunds", "Bearer "+g.admin, map[string]any{
		"payment_intent": "pi_second", "refund_id": "re_pi_second", "queue": "pi_second-0", "status": "failed",
	}, &report); code != http.StatusOK || !report.Queued {
		t.Fatalf("failed refund report = %d %+v; want it queued again", code, report)
	}
	if code := g.call("POST", "/api/v1/credits/card-refunds", "Bearer "+g.admin, map[string]any{
		"payment_intent": "pi_other", "refund_id": "re_other", "queue": "pi_other-0", "status": "failed",
	}, &report); code != http.StatusOK || report.Queued {
		t.Fatalf("an unqueued refund's report = %d %+v; want not queued", code, report)
	}
}
