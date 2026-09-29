package controller

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const teamBillingHistoryLimit = 50

// safety: Stripe keeps a session open 31 minutes from its creation.
const checkoutOpenHold = 35 * time.Minute

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
	Frozen                    bool                   `json:"frozen"`
	Trusted                   bool                   `json:"trusted"`
	PurchaseLimitCents        int64                  `json:"purchase_limit_cents"`
	Purchased30dCents         int64                  `json:"purchased_30d_cents"`
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
	CeilingCents     int64  `json:"ceiling_cents"`
	DailyCapCents    int64  `json:"daily_cap_cents"`
	BudgetCents      int64  `json:"budget_cents"`
	RungCents        int64  `json:"rung_cents"`
	CreditLimitCents int64  `json:"credit_limit_cents"`
	Spent30dCents    int64  `json:"spent_30d_cents"`
	SpentTodayCents  int64  `json:"spent_today_cents"`
	HeadroomCents    int64  `json:"headroom_cents"`
	Binding          string `json:"binding"`
}

func teamLimitsBody(st store.SpendStanding) teamLimitsJSON {
	room, limit := st.Headroom()
	c := int64(store.MicroCreditsPerCent)
	return teamLimitsJSON{
		CeilingCents: st.CeilingMicro / c, DailyCapCents: st.DailyCapMicro / c, BudgetCents: st.BudgetMicro / c,
		RungCents: st.RungMicro / c, CreditLimitCents: st.CreditLimitMicro / c,
		Spent30dCents: st.Spent30dMicro / c, SpentTodayCents: st.SpentTodayMicro / c,
		HeadroomCents: room / c, Binding: limit,
	}
}

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
	freeze, err := t.CreditFreeze(ctx)
	if err != nil {
		s.writeInternalError(w, r, "team billing freeze", err)
		return
	}
	now := time.Now()
	spend, err := t.SpendStanding(ctx, now)
	if err != nil {
		s.writeInternalError(w, r, "team billing spend", err)
		return
	}
	standing, err := t.BillingStanding(ctx, now)
	if err != nil {
		s.writeInternalError(w, r, "team billing standing", err)
		return
	}
	owner := store.Role(p.Role).AtLeast(store.RoleOwner)
	out := teamBillingJSON{
		Team:                      string(t.Team()),
		BalanceMicro:              state.BalanceMicro,
		BalanceCapMicro:           store.MaxTeamBalanceMicro,
		MicroPerCredit:            store.MicroCreditsPerCredit,
		CreditsPerDollar:          store.CreditsPerDollar,
		MinBillableSeconds:        store.MinBillableSeconds,
		PurchaseMinCents:          store.CreditPurchaseMinCents,
		PurchaseMaxCents:          standing.PurchaseMaxCents(),
		RateTable:                 creditRateTableToJSON(state.RateTable),
		StorageRateMicroPerGBDay:  state.StorageRateMicroPerGBDay,
		StorageFreeAllowanceBytes: state.StorageFreeAllowanceBytes,
		StorageChargedMicro:       state.StorageChargedMicro,
		Frozen:                    freeze.Frozen,
		Trusted:                   standing.Trusted,
		PurchaseLimitCents:        standing.LimitMicro / store.MicroCreditsPerCent,
		Purchased30dCents:         standing.PurchasedMicro / store.MicroCreditsPerCent,
		CheckoutEnabled:           s.checkout != nil,
		CanPurchase:               s.checkout != nil && owner && !spend.CardBilled(),
		CardBilled:                spend.CardBilled(),
		CanAddCard:                s.checkout != nil && owner && standing.Trusted,
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
	PurchaseLimitCode       = "purchase_limit"
)

type purchaseLimitRefusalJSON struct {
	Error          string `json:"error"`
	Code           string `json:"code"`
	Trusted        bool   `json:"trusted"`
	LimitCents     int64  `json:"limit_cents"`
	PurchasedCents int64  `json:"purchased_cents"`
	AmountCents    int64  `json:"amount_cents"`
}

type balanceCapRefusalJSON struct {
	Error        string `json:"error"`
	Code         string `json:"code"`
	BalanceMicro int64  `json:"balance_micro"`
	OpenMicro    int64  `json:"open_micro"`
	CapMicro     int64  `json:"cap_micro"`
	AmountMicro  int64  `json:"amount_micro"`
}

type codedErrorJSON struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

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
	ctx := r.Context()
	now := time.Now()
	spend, err := t.SpendStanding(ctx, now)
	if err != nil {
		s.writeInternalError(w, r, "team checkout standing", err)
		return
	}
	// safety: a team whose usage is billed to its card spends any prepaid
	// balance first and buys no more, so its card is its only way to pay; a
	// revoked team keeps its card for what it owes and prepays again.
	if spend.CardBilled() {
		writeJSON(w, http.StatusConflict, codedErrorJSON{
			Error: "this team's usage is billed to its card; it buys no prepaid credit", Code: CardOnFileCode,
		})
		return
	}
	standing := spend.Billing
	if maxCents := standing.PurchaseMaxCents(); req.AmountCents < store.CreditPurchaseMinCents || req.AmountCents > maxCents {
		writeJSON(w, http.StatusBadRequest, codedErrorJSON{
			Error: fmt.Sprintf("the minimum purchase is $%d and the maximum $%d",
				store.CreditPurchaseMinCents/100, maxCents/100),
			Code: CheckoutAmountCode,
		})
		return
	}
	// safety: the limits are held here, before any money moves, counting every
	// checkout of the team still open; the grant that follows a verified
	// payment is never refused, so a later check would strand the payment.
	checkoutID, err := t.OpenCreditCheckout(ctx, req.AmountCents*store.MicroCreditsPerCent, now, checkoutOpenHold)
	if err != nil {
		if s.writeBalanceCapRefusal(w, err) {
			return
		}
		var limitErr *store.PurchaseLimitError
		if errors.As(err, &limitErr) {
			writeJSON(w, http.StatusConflict, purchaseLimitRefusalJSON{
				Error: limitErr.Error(), Code: PurchaseLimitCode, Trusted: limitErr.Trusted,
				LimitCents:     limitErr.LimitMicro / store.MicroCreditsPerCent,
				PurchasedCents: limitErr.PurchasedMicro / store.MicroCreditsPerCent,
				AmountCents:    limitErr.AmountMicro / store.MicroCreditsPerCent,
			})
			return
		}
		if errors.Is(err, store.ErrTeamBeingDeleted) {
			writeError(w, http.StatusConflict, err)
			return
		}
		s.writeInternalError(w, r, "team checkout balance", err)
		return
	}
	session, err := s.checkout.open(ctx, string(t.Team()), req.AmountCents)
	if err != nil {
		if dropErr := t.DropCreditCheckout(ctx, checkoutID); dropErr != nil {
			s.logger.Warn("dropping an unopened checkout failed", "team", string(t.Team()), "err", dropErr)
		}
		s.logger.Error("opening a checkout session failed", "team", string(t.Team()),
			"cents", req.AmountCents, "err", err)
		writeError(w, http.StatusBadGateway, errors.New("the payment page could not be opened; try again"))
		return
	}
	expires := session.ExpiresAt
	if expires.IsZero() || expires.After(now.Add(checkoutOpenHold)) {
		expires = now.Add(checkoutOpenHold)
	}
	if err := t.AttachCreditCheckout(ctx, checkoutID, session.ID, expires); err != nil {
		s.writeInternalError(w, r, "team checkout record", err)
		return
	}
	s.logger.Info("checkout session opened", "team", string(t.Team()), "cents", req.AmountCents,
		"session", session.ID)
	writeJSON(w, http.StatusOK, teamCheckoutJSON{URL: session.URL})
}

func (s *Server) writeBalanceCapRefusal(w http.ResponseWriter, err error) bool {
	var capErr *store.CreditBalanceCapError
	if !errors.As(err, &capErr) {
		return false
	}
	writeJSON(w, http.StatusConflict, balanceCapRefusalJSON{
		Error: capErr.Error(), Code: BalanceCapCode,
		BalanceMicro: capErr.BalanceMicro, OpenMicro: capErr.OpenMicro,
		CapMicro: capErr.CapMicro, AmountMicro: capErr.AmountMicro,
	})
	return true
}
