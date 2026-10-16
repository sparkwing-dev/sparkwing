package controller

import (
	"net/http"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
	MicroPerCredit            int64                  `json:"micro_per_credit"`
	CreditsPerDollar          int64                  `json:"credits_per_dollar"`
	MinBillableSeconds        int64                  `json:"min_billable_seconds"`
	PurchaseMinCents          int64                  `json:"purchase_min_cents"`
	RateTable                 []creditRateJSON       `json:"rate_table"`
	StorageRateMicroPerGBDay  int64                  `json:"storage_rate_micro_per_gb_day"`
	StorageFreeAllowanceBytes int64                  `json:"storage_free_allowance_bytes"`
	StorageChargedMicro       int64                  `json:"storage_charged_micro"`
	Frozen                    bool                   `json:"frozen"`
	Trusted                   bool                   `json:"trusted"`
	CheckoutEnabled           bool                   `json:"checkout_enabled"`
	CanPurchase               bool                   `json:"can_purchase"`
	Card                      *teamCardJSON          `json:"card,omitempty"`
	CardBilled                bool                   `json:"card_billed"`
	CanAddCard                bool                   `json:"can_add_card"`
	OpenCharge                *teamChargeJSON        `json:"open_charge,omitempty"`
	Limits                    teamLimitsJSON         `json:"limits"`
	Usage                     []teamBillingUsageJSON `json:"usage"`
	Grants                    []teamBillingGrantJSON `json:"grants"`
}

type teamCardJSON struct {
	Brand   string `json:"brand"`
	Last4   string `json:"last4"`
	AddedAt int64  `json:"added_at"`
}

type teamChargeJSON struct {
	ID            string `json:"id"`
	AmountCents   int64  `json:"amount_cents"`
	Failures      int64  `json:"failures"`
	DeclineCode   string `json:"decline_code,omitempty"`
	NextAttemptAt int64  `json:"next_attempt_at,omitempty"`
	OpenedAt      int64  `json:"opened_at"`
}

type teamLimitsJSON struct {
	Standing        string `json:"standing"`
	CloudVCPU       int64  `json:"cloud_vcpu"`
	MaxJobSeconds   int64  `json:"max_job_seconds"`
	BudgetCents     int64  `json:"budget_cents"`
	RungCents       int64  `json:"rung_cents"`
	Spent30dCents   int64  `json:"spent_30d_cents"`
	SpentTodayCents int64  `json:"spent_today_cents"`
	HeadroomCents   int64  `json:"headroom_cents"`
	Binding         string `json:"binding"`
}

func teamLimitsBody(st store.SpendStanding) teamLimitsJSON {
	room, limit := st.Headroom()
	c := int64(store.MicroCreditsPerCent)
	return teamLimitsJSON{
		Standing: st.Billing.Tier, CloudVCPU: st.Billing.CloudVCPU, MaxJobSeconds: int64(st.Billing.MaxJobDuration.Seconds()),
		BudgetCents:   st.BudgetMicro / c,
		RungCents:     st.RungMicro / c,
		Spent30dCents: st.Spent30dMicro / c, SpentTodayCents: st.SpentTodayMicro / c,
		HeadroomCents: room / c, Binding: limit,
	}
}

func (s *Server) handleTeamBilling(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if !s.Metering() {
		return &apierr.Error{Code: apierr.NotFound, Message: "billing is not enabled"}
	}
	t, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	ctx := r.Context()
	state, err := t.CreditState(ctx, creditsBurnWindow)
	if err != nil {
		return engineLookupError(err)
	}
	usage, err := t.CreditUsageByRun(ctx, teamBillingHistoryLimit)
	if err != nil {
		return engineLookupError(err)
	}
	grants, err := t.ListCreditGrants(ctx, teamBillingHistoryLimit)
	if err != nil {
		return engineLookupError(err)
	}
	freeze, err := t.CreditFreeze(ctx)
	if err != nil {
		return engineLookupError(err)
	}
	now := time.Now()
	spend, err := t.SpendStanding(ctx, now)
	if err != nil {
		return engineLookupError(err)
	}
	standing, err := t.BillingStanding(ctx, now)
	if err != nil {
		return engineLookupError(err)
	}
	owner := slices.Contains(a.Principal.Capability.Grants, capability.TeamAdmin)
	out := teamBillingJSON{
		Team:                      string(t.Team()),
		BalanceMicro:              state.BalanceMicro,
		MicroPerCredit:            store.MicroCreditsPerCredit,
		CreditsPerDollar:          store.CreditsPerDollar,
		MinBillableSeconds:        store.MinBillableSeconds,
		PurchaseMinCents:          store.MinCardChargeCents,
		RateTable:                 creditRateTableToJSON(state.RateTable),
		StorageRateMicroPerGBDay:  state.StorageRateMicroPerGBDay,
		StorageFreeAllowanceBytes: state.StorageFreeAllowanceBytes,
		StorageChargedMicro:       state.StorageChargedMicro,
		Frozen:                    freeze.Frozen,
		Trusted:                   standing.Trusted,
		CheckoutEnabled:           s.checkout != nil,
		CanPurchase:               s.checkout != nil && owner,
		CardBilled:                spend.CardBilled(),
		CanAddCard:                s.checkout != nil && owner,
		Limits:                    teamLimitsBody(spend),
		Usage:                     make([]teamBillingUsageJSON, 0, len(usage)),
		Grants:                    make([]teamBillingGrantJSON, 0, len(grants)),
	}
	if spend.Card.PaymentMethod != "" {
		out.Card = &teamCardJSON{Brand: spend.Card.Brand, Last4: spend.Card.Last4, AddedAt: spend.Card.AddedAt.Unix()}
	}
	if c := spend.OpenCharge; c != nil {
		out.OpenCharge = &teamChargeJSON{
			ID: c.ID, AmountCents: c.AmountMicro / store.MicroCreditsPerCent, Failures: c.Failures,
			DeclineCode: c.DeclineCode, OpenedAt: c.OpenedAt.Unix(),
		}
		if c.Failures > 0 && c.NextAttemptAt.Year() < 9999 {
			out.OpenCharge.NextAttemptAt = c.NextAttemptAt.Unix()
		}
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
	return nil
}

type teamCheckoutReq struct {
	AmountCents int64 `json:"amount_cents"`
}

// Machine-readable codes on a refused credit purchase.
const (
	CheckoutAmountCode      = "amount_out_of_range"
	CheckoutUnavailableCode = "checkout_unavailable"
)

func (s *Server) handleTeamBillingCheckout(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var req teamCheckoutReq
	if err := decodeEngineRequest(r, &req); err != nil {
		return err
	}
	return s.openBillingPage(w, r, a, capability.BillingPageCredits, req.AmountCents)
}
