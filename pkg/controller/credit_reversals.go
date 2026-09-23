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

// handleCreditUnits answers the units the ledger prices in, so the checkout
// service converts a payment into the micro-credits this controller means.
func (s *Server) handleCreditUnits(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, creditUnitsJSON{
		MicroPerCredit:   store.MicroCreditsPerCredit,
		CreditsPerDollar: store.CreditsPerDollar,
		MicroPerCent:     store.MicroCreditsPerCent,
	})
}

type reversePaymentReq struct {
	PaymentID string `json:"payment_id"`
	// Reference names this reversal, such as "refund:<payment id>" for an
	// operator's refund or the dispute id for a lost chargeback; a repeat
	// under it returns the reversal already written.
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

// handleReversePayment takes back what a payment still has on the ledger, in
// the team it funded. An operator's refund and a lost chargeback both come
// here; the balance may go below zero, which stops the team's metered work.
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
	// PaymentID names the team by a payment it made, which is how the
	// checkout service names a disputed team. Team names it directly and is
	// the operator's alone.
	PaymentID string `json:"payment_id,omitempty"`
	Team      string `json:"team,omitempty"`
	Frozen    bool   `json:"frozen"`
	Reason    string `json:"reason,omitempty"`
}

type creditFreezeJSON struct {
	Team   string `json:"team"`
	Frozen bool   `json:"frozen"`
	Reason string `json:"reason,omitempty"`
}

// handleCreditFreeze holds or releases a team's cloud usage. A held team's
// metered claims are refused until it is released.
func (s *Server) handleCreditFreeze(w http.ResponseWriter, r *http.Request) {
	var req creditFreezeReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.PaymentID, req.Team = strings.TrimSpace(req.PaymentID), strings.TrimSpace(req.Team)
	if (req.PaymentID == "") == (req.Team == "") {
		writeError(w, http.StatusBadRequest, errors.New("name exactly one of payment_id or team"))
		return
	}
	// safety: the checkout service acts only on teams that paid through it,
	// so a team named directly is the operator's to hold.
	if req.Team != "" && !isAdmin(r) {
		writeError(w, http.StatusForbidden, fmt.Errorf("naming a team needs the %s scope; name the payment", ScopeAdmin))
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
	if err := s.store.SetTeamCreditFreeze(r.Context(), team, req.Frozen, req.Reason, time.Now()); err != nil {
		if errors.Is(err, store.ErrUnknownTeam) {
			writeError(w, http.StatusNotFound, fmt.Errorf("team %q is not registered", team))
			return
		}
		s.writeInternalError(w, r, "freeze team", err)
		return
	}
	s.logger.Warn("team credit freeze changed", "team", string(team), "frozen", req.Frozen,
		"reason", req.Reason, "payment_id", req.PaymentID, "by", principalName(r))
	out := creditFreezeJSON{Team: string(team), Frozen: req.Frozen}
	if req.Frozen {
		out.Reason = req.Reason
	}
	writeJSON(w, http.StatusOK, out)
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
