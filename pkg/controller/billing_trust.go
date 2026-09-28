package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type billingTrustReq struct {
	Trust      string `json:"trust"`
	Reason     string `json:"reason"`
	LimitCents int64  `json:"limit_cents,omitempty"`
}

type billingTrustJSON struct {
	Team               string `json:"team"`
	Trust              string `json:"trust"`
	Trusted            bool   `json:"trusted"`
	TrustBy            string `json:"trust_by,omitempty"`
	TrustAt            int64  `json:"trust_at,omitempty"`
	TrustReason        string `json:"trust_reason,omitempty"`
	LimitOverrideCents int64  `json:"limit_override_cents,omitempty"`
	PurchaseLimitCents int64  `json:"purchase_limit_cents"`
	Purchased30dCents  int64  `json:"purchased_30d_cents"`
}

func billingTrustBody(team store.Team, b store.BillingStanding) billingTrustJSON {
	out := billingTrustJSON{
		Team: string(team), Trust: b.Trust, Trusted: b.Trusted, TrustBy: b.TrustBy,
		TrustReason: b.TrustReason, LimitOverrideCents: b.LimitOverrideCents,
		PurchaseLimitCents: b.LimitMicro / store.MicroCreditsPerCent,
		Purchased30dCents:  b.PurchasedMicro / store.MicroCreditsPerCent,
	}
	if !b.TrustAt.IsZero() {
		out.TrustAt = b.TrustAt.Unix()
	}
	return out
}

func (s *Server) handleBillingTrustShow(w http.ResponseWriter, r *http.Request) {
	t, ok := s.namedTenant(w, r, r.PathValue("team"))
	if !ok {
		return
	}
	b, err := t.BillingStanding(r.Context(), time.Now())
	if err != nil {
		s.writeInternalError(w, r, "billing trust", err)
		return
	}
	writeJSON(w, http.StatusOK, billingTrustBody(t.Team(), b))
}

func (s *Server) handleBillingTrustSet(w http.ResponseWriter, r *http.Request) {
	t, ok := s.namedTenant(w, r, r.PathValue("team"))
	if !ok {
		return
	}
	var req billingTrustReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	actor := ""
	if p, ok := PrincipalFromContext(r.Context()); ok {
		actor = p.Name
	}
	_, after, err := t.SetBillingTrust(r.Context(), store.BillingTrustChange{
		Trust: req.Trust, Actor: actor, Reason: req.Reason, LimitCents: req.LimitCents,
	}, time.Now())
	switch {
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
		return
	case err != nil:
		s.writeInternalError(w, r, "billing trust change", err)
		return
	}
	writeJSON(w, http.StatusOK, billingTrustBody(t.Team(), after))
}
