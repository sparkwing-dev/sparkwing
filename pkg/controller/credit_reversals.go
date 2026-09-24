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
}

// safety: Checkout and ledger must agree on the micro-credit unit before converting a payment.
func (s *Server) handleCreditUnits(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, creditUnitsJSON{
		MicroPerCredit:   store.MicroCreditsPerCredit,
		CreditsPerDollar: store.CreditsPerDollar,
		MicroPerCent:     store.MicroCreditsPerCent,
	})
}

type reversePaymentReq struct {
	PaymentID string `json:"payment_id"`
	// safety: A reversal reference is its idempotency key for refunds and chargebacks.
	Reference string `json:"reference"`
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
	who := principalName(r)
	res, err := s.store.ReversePayment(r.Context(), req.PaymentID, req.Reference, who)
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
