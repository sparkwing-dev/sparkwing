package controller_test

import (
	"net/http"
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
		BudgetCents int64  `json:"budget_cents"`
		Binding     string `json:"binding"`
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
