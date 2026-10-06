package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type creditUnitsJSON struct {
	MicroPerCredit   int64 `json:"micro_per_credit"`
	CreditsPerDollar int64 `json:"credits_per_dollar"`
	MicroPerCent     int64 `json:"micro_per_cent"`
	// safety: the checkout service refuses a card-billing write to a
	// controller that does not name its contract here, rather than have an
	// older controller misread it.
	Capabilities []string `json:"capabilities"`
}

// CardBillingCapability is the contract of partial reversals, saved cards,
// card payments and payment warnings.
const CardBillingCapability = "card-billing-v1"

// PaidGrantLookupCapability names GET /api/v1/credits/payments/{reference},
// which reports the team a payment was granted to without writing anything.
const PaidGrantLookupCapability = "paid-grant-lookup-v1"

// UnknownPaymentCode is the code on the payment lookup's 404 when no paid
// grant carries the reference. Any other 404 means the route is not served.
const UnknownPaymentCode = "unknown_payment"

// safety: Checkout and ledger must agree on the micro-credit unit before converting a payment.
func (s *Server) handleCreditUnits(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, creditUnitsJSON{
		MicroPerCredit:   store.MicroCreditsPerCredit,
		CreditsPerDollar: store.CreditsPerDollar,
		MicroPerCent:     store.MicroCreditsPerCent,
		Capabilities:     []string{CardBillingCapability, PaidGrantLookupCapability},
	})
}

type paidGrantJSON struct {
	Team string `json:"team"`
}

// safety: the checkout service asks this before refunding a refused card, so
// a payment the ledger already settled is not refunded after a lost reply.
func (s *Server) handlePaidGrantLookup(w http.ResponseWriter, r *http.Request) {
	team, found, err := s.store.PaidGrantTeam(r.Context(), r.PathValue("reference"))
	if err != nil {
		s.writeInternalError(w, r, "paid grant lookup", err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, codedErrorJSON{Error: "no paid grant carries this reference", Code: UnknownPaymentCode})
		return
	}
	writeJSON(w, http.StatusOK, paidGrantJSON{Team: string(team)})
}

type reversePaymentReq struct {
	PaymentID string `json:"payment_id"`
	// safety: A reversal reference is its idempotency key for refunds and chargebacks.
	Reference string `json:"reference"`
	// safety: zero reverses what remains, which is how a lost dispute takes
	// back the whole payment; a refund names exactly its own amount.
	AmountMicro int64 `json:"amount_micro,omitempty"`
}

type reversePaymentJSON struct {
	Team          string           `json:"team"`
	PaymentID     string           `json:"payment_id"`
	PaidMicro     int64            `json:"paid_micro"`
	ReversedMicro int64            `json:"reversed_micro"`
	BalanceMicro  int64            `json:"balance_micro"`
	Created       bool             `json:"created"`
	Grant         *creditGrantJSON `json:"grant,omitempty"`
}

// safety: A refund or lost chargeback may make the funded team's balance negative and stop metered work.
func (s *Server) handleReversePayment(w http.ResponseWriter, r *http.Request) {
	var req reversePaymentReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.PaymentID, req.Reference = strings.TrimSpace(req.PaymentID), strings.TrimSpace(req.Reference)
	if req.PaymentID == "" || req.Reference == "" {
		writeError(w, http.StatusBadRequest, errors.New("payment_id and reference are required"))
		return
	}
	if req.AmountMicro < 0 {
		writeError(w, http.StatusBadRequest, errors.New("amount_micro must not be negative"))
		return
	}
	who := principalName(r)
	res, err := s.store.ReversePayment(r.Context(), req.PaymentID, req.Reference, who, req.AmountMicro)
	switch {
	case errors.Is(err, store.ErrUnknownPayment):
		writeError(w, http.StatusNotFound, err)
		return
	case s.writeDisputeConflict(w, r, err, req.Reference, req.PaymentID):
		return
	case errors.Is(err, store.ErrCreditGrantConflict):
		writeError(w, http.StatusConflict, err)
		return
	case err != nil:
		s.writeInternalError(w, r, "reverse payment", err)
		return
	}
	out := reversePaymentJSON{
		Team: string(res.Team), PaymentID: req.PaymentID, PaidMicro: res.PaidMicro,
		ReversedMicro: res.ReversedMicro, BalanceMicro: res.BalanceMicro, Created: res.Created,
	}
	if res.Grant != nil {
		g := creditGrantToJSON(*res.Grant)
		out.Grant = &g
	}
	s.logger.Warn("payment reversed", "team", out.Team, "payment_id", req.PaymentID,
		"reference", req.Reference, "created", res.Created, "reversed_micro", res.ReversedMicro,
		"balance_micro", res.BalanceMicro, "by", who)
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}

type creditFreezeReq struct {
	// safety: A payment identifies its team; naming a team directly is reserved for the operator.
	PaymentID string `json:"payment_id,omitempty"`
	Team      string `json:"team,omitempty"`
	// safety: Each dispute owns one hold, so releasing it leaves other holds intact.
	DisputeID string `json:"dispute_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
	// safety: Only the operator may release all holds by omitting a dispute ID.
	Release bool `json:"release,omitempty"`
}

type creditFreezeJSON struct {
	Team     string   `json:"team"`
	Frozen   bool     `json:"frozen"`
	Disputes []string `json:"disputes"`
	Released int64    `json:"released,omitempty"`
}

// safety: Any remaining dispute hold blocks the team's metered claims.
func (s *Server) handleCreditFreeze(w http.ResponseWriter, r *http.Request) {
	var req creditFreezeReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.PaymentID, req.Team = strings.TrimSpace(req.PaymentID), strings.TrimSpace(req.Team)
	req.DisputeID = strings.TrimSpace(req.DisputeID)
	// safety: the checkout service holds only a team that paid through it and
	// never releases one; a named team and every release are the operator's.
	if (req.Team != "" || req.Release) && !isAdmin(r) {
		writeError(w, http.StatusForbidden, fmt.Errorf(
			"naming a team or releasing a hold needs the %s scope", ScopeAdmin))
		return
	}
	if req.PaymentID != "" && req.Team != "" {
		writeError(w, http.StatusBadRequest, errors.New("name the team by payment_id or by team, not both"))
		return
	}
	team := store.Team(req.Team)
	if req.PaymentID != "" {
		paid, err := s.store.PaymentTeam(r.Context(), req.PaymentID)
		if errors.Is(err, store.ErrUnknownPayment) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		if err != nil {
			s.writeInternalError(w, r, "freeze payment team", err)
			return
		}
		team = paid
	}
	now := time.Now()
	out := creditFreezeJSON{}
	if req.Release {
		if team == "" && req.DisputeID == "" {
			writeError(w, http.StatusBadRequest, errors.New("a release names a team, a dispute_id, or both"))
			return
		}
		if team == "" {
			held, found, err := s.store.DisputeTeam(r.Context(), req.DisputeID)
			if err != nil {
				s.writeInternalError(w, r, "freeze dispute team", err)
				return
			}
			if !found {
				writeError(w, http.StatusNotFound, fmt.Errorf("no hold names dispute %q", req.DisputeID))
				return
			}
			team = held
		}
		n, err := s.store.ReleaseCreditFreezes(r.Context(), team, req.DisputeID, now)
		if err != nil {
			s.writeInternalError(w, r, "release freeze", err)
			return
		}
		out.Released = n
	} else {
		if team == "" || req.DisputeID == "" {
			writeError(w, http.StatusBadRequest, errors.New("a hold names the team, by payment_id or team, and the dispute_id"))
			return
		}
		if _, err := s.store.HoldTeamForDispute(r.Context(), team, req.DisputeID, req.PaymentID, req.Reason, now); err != nil {
			if s.writeDisputeConflict(w, r, err, req.DisputeID, req.PaymentID) {
				return
			}
			if errors.Is(err, store.ErrUnknownTeam) {
				writeError(w, http.StatusNotFound, fmt.Errorf("team %q is not registered", team))
				return
			}
			s.writeInternalError(w, r, "hold team", err)
			return
		}
	}
	tenant, ok := s.namedTenant(w, r, string(team))
	if !ok {
		return
	}
	freeze, err := tenant.CreditFreeze(r.Context())
	if err != nil {
		s.writeInternalError(w, r, "read freeze", err)
		return
	}
	out.Team, out.Frozen, out.Disputes = string(team), freeze.Frozen, freeze.Disputes
	if out.Disputes == nil {
		out.Disputes = []string{}
	}
	s.logger.Warn("team credit freeze changed", "team", out.Team, "release", req.Release,
		"dispute_id", req.DisputeID, "payment_id", req.PaymentID, "frozen", out.Frozen,
		"released", out.Released, "by", principalName(r))
	writeJSON(w, http.StatusOK, out)
}

type checkoutClosedReq struct {
	Team      string `json:"team"`
	SessionID string `json:"session_id"`
	Outcome   string `json:"outcome"`
}

var checkoutOutcomeEvents = map[string]string{
	"failed":  store.BusinessEventCheckoutFailed,
	"expired": store.BusinessEventCheckoutExpired,
}

// safety: only the checkout service reports how its sessions ended, and a
// report closes nothing but an open, unpaid checkout the named team holds for
// that session, so it cannot release another team's hold or unpay a payment.
func (s *Server) handleCheckoutClosed(w http.ResponseWriter, r *http.Request) {
	if p, ok := PrincipalFromContext(r.Context()); ok && !p.HasScope(ScopeCreditsGrant) {
		writeError(w, http.StatusForbidden, fmt.Errorf("closing a checkout needs the %s scope itself", ScopeCreditsGrant))
		return
	}
	var req checkoutClosedReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Team, req.SessionID = strings.TrimSpace(req.Team), strings.TrimSpace(req.SessionID)
	kind, ok := checkoutOutcomeEvents[req.Outcome]
	if !ok || req.Team == "" || req.SessionID == "" {
		writeError(w, http.StatusBadRequest, errors.New("team, session_id and an outcome of failed or expired are required"))
		return
	}
	tenant, ok := s.namedTenant(w, r, req.Team)
	if !ok {
		return
	}
	closed, err := tenant.CloseCreditCheckout(r.Context(), req.SessionID, kind, principalName(r), time.Now())
	if errors.Is(err, store.ErrCheckoutNotFound) {
		writeJSON(w, http.StatusNotFound, codedErrorJSON{Error: err.Error(), Code: UnknownCheckoutCode})
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "close checkout", err)
		return
	}
	s.logger.Info("checkout closed unpaid", "team", req.Team, "outcome", req.Outcome, "closed", closed)
	writeJSON(w, http.StatusOK, checkoutClosedJSON{
		Team: req.Team, SessionID: req.SessionID, Outcome: req.Outcome, Closed: closed,
	})
}

// UnknownCheckoutCode is the code on the 404 a checkout closure gets when
// the team holds no checkout for the session, which no retry can change.
const UnknownCheckoutCode = "unknown_checkout"

type checkoutClosedJSON struct {
	Team      string `json:"team"`
	SessionID string `json:"session_id"`
	Outcome   string `json:"outcome"`
	Closed    bool   `json:"closed"`
}

// DisputeConflictCode is the code on a 409 refusing a hold or a reversal
// that names a dispute already held for another payment.
const DisputeConflictCode = "dispute_conflict"

// safety: a dispute disputes one payment, so the same id naming another
// payment is a misrouted or forged event; it is refused and alerted on,
// never applied to a second team.
func (s *Server) writeDisputeConflict(w http.ResponseWriter, r *http.Request, err error, disputeID, paymentID string) bool {
	if !errors.Is(err, store.ErrDisputeConflict) {
		return false
	}
	s.logger.Error("billing alert: a dispute id arrived for a payment it does not dispute; refused",
		"alert", DisputeConflictCode, "dispute_id", disputeID, "payment_id", paymentID,
		"by", principalName(r), "err", err)
	writeJSON(w, http.StatusConflict, codedErrorJSON{Error: err.Error(), Code: DisputeConflictCode})
	return true
}

func principalName(r *http.Request) string {
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil {
		return p.Name
	}
	return authwire.AnonymousPrincipal
}

func isAdmin(r *http.Request) bool {
	p, ok := PrincipalFromContext(r.Context())
	return !ok || p.HasScope(ScopeAdmin)
}
