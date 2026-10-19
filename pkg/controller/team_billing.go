package controller

import (
	"net/http"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	wire "github.com/sparkwing-dev/sparkwing/internal/controllerwire"
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
	SliceSeconds              int64                  `json:"slice_seconds"`
	PurchaseMinCents          int64                  `json:"purchase_min_cents"`
	RateTable                 []creditRateJSON       `json:"rate_table"`
	StorageRateMicroPerGBDay  int64                  `json:"storage_rate_micro_per_gb_day"`
	StorageFreeAllowanceBytes int64                  `json:"storage_free_allowance_bytes"`
	StorageChargedMicro       int64                  `json:"storage_charged_micro"`
	Frozen                    bool                   `json:"frozen"`
	HoldCause                 string                 `json:"hold_cause,omitempty"`
	Trusted                   bool                   `json:"trusted"`
	CheckoutEnabled           bool                   `json:"checkout_enabled"`
	CanPurchase               bool                   `json:"can_purchase"`
	Card                      *teamCardJSON          `json:"card,omitempty"`
	CanAddCard                bool                   `json:"can_add_card"`
	Refill                    teamRefillJSON         `json:"refill"`
	Limits                    teamLimitsJSON         `json:"limits"`
	Standing                  wire.TeamStanding      `json:"standing"`
	Usage                     []teamBillingUsageJSON `json:"usage"`
	Grants                    []teamBillingGrantJSON `json:"grants"`
}

type teamCardJSON struct {
	Brand   string `json:"brand"`
	Last4   string `json:"last4"`
	AddedAt int64  `json:"added_at"`
}

type teamRefillJSON struct {
	Enabled         bool               `json:"enabled"`
	ThresholdCents  int64              `json:"threshold_cents"`
	AmountCents     int64              `json:"amount_cents"`
	WeekLimitCents  int64              `json:"week_limit_cents"`
	MonthLimitCents int64              `json:"month_limit_cents"`
	MinAmountCents  int64              `json:"min_amount_cents"`
	MaxAmountCents  int64              `json:"max_amount_cents"`
	Caps            teamRefillCapsJSON `json:"caps"`
	FailedAt        int64              `json:"failed_at,omitempty"`
	Failure         string             `json:"failure,omitempty"`
	Open            bool               `json:"open"`
	DayCount        int64              `json:"day_count"`
	WeekCents       int64              `json:"week_cents"`
	MonthCents      int64              `json:"month_cents"`
	Limit           string             `json:"limit,omitempty"`
}

type teamRefillCapsJSON struct {
	AmountCents int64 `json:"amount_cents"`
	PerDay      int64 `json:"per_day"`
	WeekCents   int64 `json:"week_cents"`
}

func teamRefillBody(r store.RefillState) teamRefillJSON {
	out := teamRefillJSON{
		Enabled: r.Enabled, ThresholdCents: r.ThresholdCents, AmountCents: r.AmountCents,
		WeekLimitCents: r.WeekLimitCents, MonthLimitCents: r.MonthLimitCents,
		MinAmountCents: store.MinRefillCents, MaxAmountCents: store.MaxRefillCents,
		Caps:    teamRefillCapsJSON{AmountCents: r.Caps.AmountCents, PerDay: r.Caps.PerDay, WeekCents: r.Caps.WeekCents},
		Failure: r.Failure, Open: r.Open, DayCount: r.DayCount, WeekCents: r.WeekCents, MonthCents: r.MonthCents, Limit: r.Limit,
	}
	if !r.FailedAt.IsZero() {
		out.FailedAt = r.FailedAt.Unix()
	}
	return out
}

type teamLimitsJSON struct {
	Standing      string `json:"standing"`
	CloudVCPU     int64  `json:"cloud_vcpu"`
	MaxJobSeconds int64  `json:"max_job_seconds"`
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
	teamStanding, err := teamStandingBody(ctx, t, standing, owner)
	if err != nil {
		return engineLookupError(err)
	}
	out := teamBillingJSON{
		Team:                      string(t.Team()),
		BalanceMicro:              state.BalanceMicro,
		MicroPerCredit:            store.MicroCreditsPerCredit,
		CreditsPerDollar:          store.CreditsPerDollar,
		SliceSeconds:              store.SliceSeconds,
		PurchaseMinCents:          store.MinCardChargeCents,
		RateTable:                 creditRateTableToJSON(state.RateTable),
		StorageRateMicroPerGBDay:  state.StorageRateMicroPerGBDay,
		StorageFreeAllowanceBytes: state.StorageFreeAllowanceBytes,
		StorageChargedMicro:       state.StorageChargedMicro,
		Frozen:                    freeze.Frozen,
		HoldCause:                 standing.HoldCause,
		Trusted:                   standing.Trusted,
		CheckoutEnabled:           s.checkout != nil,
		CanPurchase:               s.checkout != nil && owner,
		CanAddCard:                s.checkout != nil && owner,
		Refill:                    teamRefillBody(spend.Refill),
		Limits: teamLimitsJSON{
			Standing: standing.Tier, CloudVCPU: standing.CloudVCPU, MaxJobSeconds: int64(standing.MaxJobDuration.Seconds()),
		},
		Standing: teamStanding,
		Usage:    make([]teamBillingUsageJSON, 0, len(usage)),
		Grants:   make([]teamBillingGrantJSON, 0, len(grants)),
	}
	if spend.Card.PaymentMethod != "" {
		out.Card = &teamCardJSON{Brand: spend.Card.Brand, Last4: spend.Card.Last4, AddedAt: spend.Card.AddedAt.Unix()}
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
