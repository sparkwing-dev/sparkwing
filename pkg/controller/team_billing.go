package controller

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// teamBillingHistoryLimit is how many runs and grants the billing page lists.
const teamBillingHistoryLimit = 50

type teamBillingUsageJSON struct {
	RunID         string `json:"run_id"`
	Seconds       int64  `json:"seconds"`
	AmountMicro   int64  `json:"amount_micro"`
	LastChargedAt int64  `json:"last_charged_at"`
}

type teamBillingGrantJSON struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	AmountMicro int64  `json:"amount_micro"`
	Reference   string `json:"reference,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

type teamBillingJSON struct {
	Team                      string                 `json:"team"`
	BalanceMicro              int64                  `json:"balance_micro"`
	BalanceCapMicro           int64                  `json:"balance_cap_micro"`
	MicroPerCredit            int64                  `json:"micro_per_credit"`
	CreditsPerDollar          int64                  `json:"credits_per_dollar"`
	MinBillableSeconds        int64                  `json:"min_billable_seconds"`
	PurchaseMinCents          int64                  `json:"purchase_min_cents"`
	PurchaseMaxCents          int64                  `json:"purchase_max_cents"`
	RateTable                 []creditRateJSON       `json:"rate_table"`
	StorageRateMicroPerGBDay  int64                  `json:"storage_rate_micro_per_gb_day"`
	StorageFreeAllowanceBytes int64                  `json:"storage_free_allowance_bytes"`
	StorageChargedMicro       int64                  `json:"storage_charged_micro"`
	CheckoutEnabled           bool                   `json:"checkout_enabled"`
	CanPurchase               bool                   `json:"can_purchase"`
	Usage                     []teamBillingUsageJSON `json:"usage"`
	Grants                    []teamBillingGrantJSON `json:"grants"`
}

// handleTeamBilling is the active team's billing page: its balance, the
// prices it pays, recent runner usage by run, and the grants that funded it.
// Any member reads it; only an owner may buy.
func (s *Server) handleTeamBilling(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	ctx := r.Context()
	state, err := t.CreditState(ctx, creditsBurnWindow)
	if err != nil {
		s.writeInternalError(w, r, "team billing state", err)
		return
	}
	usage, err := t.CreditUsageByRun(ctx, teamBillingHistoryLimit)
	if err != nil {
		s.writeInternalError(w, r, "team billing usage", err)
		return
	}
	grants, err := t.ListCreditGrants(ctx, teamBillingHistoryLimit)
	if err != nil {
		s.writeInternalError(w, r, "team billing grants", err)
		return
	}
	out := teamBillingJSON{
		Team:                      string(t.Team()),
		BalanceMicro:              state.BalanceMicro,
		BalanceCapMicro:           store.MaxTeamBalanceMicro,
		MicroPerCredit:            store.MicroCreditsPerCredit,
		CreditsPerDollar:          store.CreditsPerDollar,
		MinBillableSeconds:        store.MinBillableSeconds,
		PurchaseMinCents:          store.CreditPurchaseMinCents,
		PurchaseMaxCents:          store.CreditPurchaseMaxCents,
		RateTable:                 creditRateTableToJSON(state.RateTable),
		StorageRateMicroPerGBDay:  state.StorageRateMicroPerGBDay,
		StorageFreeAllowanceBytes: state.StorageFreeAllowanceBytes,
		StorageChargedMicro:       state.StorageChargedMicro,
		CheckoutEnabled:           s.checkout != nil,
		CanPurchase:               s.checkout != nil && store.Role(p.Role).AtLeast(store.RoleOwner),
		Usage:                     make([]teamBillingUsageJSON, 0, len(usage)),
		Grants:                    make([]teamBillingGrantJSON, 0, len(grants)),
	}
	for _, u := range usage {
		out.Usage = append(out.Usage, teamBillingUsageJSON{
			RunID: u.RunID, Seconds: u.Seconds, AmountMicro: u.AmountMicro,
			LastChargedAt: u.LastChargedAt.Unix(),
		})
	}
	for _, g := range grants {
		out.Grants = append(out.Grants, teamBillingGrantJSON{
			ID: g.ID, Kind: g.Kind, AmountMicro: g.AmountMicro, Reference: g.Reference,
			CreatedAt: g.CreatedAt.Unix(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type teamCheckoutReq struct {
	AmountCents int64 `json:"amount_cents"`
}

type teamCheckoutJSON struct {
	URL string `json:"url"`
}

// Machine-readable codes on a refused credit purchase.
const (
	CheckoutAmountCode      = "amount_out_of_range"
	CheckoutUnavailableCode = "checkout_unavailable"
	BalanceCapCode          = "balance_cap"
)

type balanceCapRefusalJSON struct {
	Error        string `json:"error"`
	Code         string `json:"code"`
	BalanceMicro int64  `json:"balance_micro"`
	CapMicro     int64  `json:"cap_micro"`
	AmountMicro  int64  `json:"amount_micro"`
}

type codedErrorJSON struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// handleTeamBillingCheckout opens a Stripe Checkout Session for the active
// team through the checkout service and answers with the page to send the
// owner to. The team comes from the owner's session, never from the body.
func (s *Server) handleTeamBillingCheckout(w http.ResponseWriter, r *http.Request) {
	_, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	if s.checkout == nil {
		writeJSON(w, http.StatusServiceUnavailable, codedErrorJSON{
			Error: "this controller sells no credits", Code: CheckoutUnavailableCode,
		})
		return
	}
	var req teamCheckoutReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.AmountCents < store.CreditPurchaseMinCents || req.AmountCents > store.CreditPurchaseMaxCents {
		writeJSON(w, http.StatusBadRequest, codedErrorJSON{
			Error: fmt.Sprintf("a purchase is between $%d and $%d",
				store.CreditPurchaseMinCents/100, store.CreditPurchaseMaxCents/100),
			Code: CheckoutAmountCode,
		})
		return
	}
	// safety: the cap is checked here, before any money moves, because the
	// webhook that grants is too late to refuse a payment without a refund.
	if err := t.CheckCreditPurchase(r.Context(), req.AmountCents*store.MicroCreditsPerCent); err != nil {
		if s.writeBalanceCapRefusal(w, err) {
			return
		}
		s.writeInternalError(w, r, "team checkout balance", err)
		return
	}
	url, err := s.checkout.open(r.Context(), string(t.Team()), req.AmountCents)
	if err != nil {
		s.logger.Error("opening a checkout session failed", "team", string(t.Team()),
			"cents", req.AmountCents, "err", err)
		writeError(w, http.StatusBadGateway, errors.New("the payment page could not be opened; try again"))
		return
	}
	s.logger.Info("checkout session opened", "team", string(t.Team()), "cents", req.AmountCents)
	writeJSON(w, http.StatusOK, teamCheckoutJSON{URL: url})
}

// writeBalanceCapRefusal answers a grant or purchase the team balance cap
// refused with 409 and the figures on both sides.
func (s *Server) writeBalanceCapRefusal(w http.ResponseWriter, err error) bool {
	var capErr *store.CreditBalanceCapError
	if !errors.As(err, &capErr) {
		return false
	}
	writeJSON(w, http.StatusConflict, balanceCapRefusalJSON{
		Error: capErr.Error(), Code: BalanceCapCode,
		BalanceMicro: capErr.BalanceMicro, CapMicro: capErr.CapMicro, AmountMicro: capErr.AmountMicro,
	})
	return true
}
