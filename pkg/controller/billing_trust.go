package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type billingTrustReq struct {
	Trust      string `json:"trust"`
	Reason     string `json:"reason"`
	LimitCents int64  `json:"limit_cents,omitempty"`
	// safety: zero keeps the level's default daily cap and rung, the debt
	// at which the card is charged.
	DailyCapCents int64 `json:"daily_cap_cents,omitempty"`
	RungCents     int64 `json:"rung_cents,omitempty"`
	RunnerCap     int64 `json:"runner_cap,omitempty"`
}

type billingTrustJSON struct {
	Team               string `json:"team"`
	Trust              string `json:"trust"`
	Trusted            bool   `json:"trusted"`
	TrustBy            string `json:"trust_by,omitempty"`
	TrustAt            int64  `json:"trust_at,omitempty"`
	TrustReason        string `json:"trust_reason,omitempty"`
	LimitOverrideCents int64  `json:"limit_override_cents,omitempty"`
	DailyCapCents      int64  `json:"daily_cap_cents,omitempty"`
	RungCents          int64  `json:"rung_cents,omitempty"`
	RunnerCap          int64  `json:"runner_cap,omitempty"`
	PurchaseLimitCents int64  `json:"purchase_limit_cents"`
	Purchased30dCents  int64  `json:"purchased_30d_cents"`
}

// safety: the wire names the automatic rule rather than sending an empty
// string, so an omitted trust field can never reset a team's trust.
const trustAutomatic = "automatic"

func billingTrustBody(team store.Team, b store.BillingStanding) billingTrustJSON {
	trust := b.Trust
	if trust == store.BillingTrustAutomatic {
		trust = trustAutomatic
	}
	out := billingTrustJSON{
		Team: string(team), Trust: trust, Trusted: b.Trusted, TrustBy: b.TrustBy,
		TrustReason: b.TrustReason, LimitOverrideCents: b.LimitOverrideCents,
		DailyCapCents: b.DailyCapOverrideCents, RungCents: b.RungOverrideCents, RunnerCap: b.RunnerCapOverride,
		PurchaseLimitCents: b.LimitMicro / store.MicroCreditsPerCent,
		Purchased30dCents:  b.PurchasedMicro / store.MicroCreditsPerCent,
	}
	if !b.TrustAt.IsZero() {
		out.TrustAt = b.TrustAt.Unix()
	}
	return out
}

func (s *Server) handleBillingTrustSet(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	t, err := s.consoleTenant(r.Context(), authorized.Target.Team)
	if err != nil {
		return err
	}
	var req billingTrustReq
	if err := decodeJSON(r, &req); err != nil {
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	}
	trust := req.Trust
	switch trust {
	case trustAutomatic:
		trust = store.BillingTrustAutomatic
	case store.BillingTrustAutomatic:
		return &apierr.Error{Code: apierr.BadRequest, Message: "trust must be granted, revoked or automatic"}
	}
	actor := authorized.Principal.Capability.Subject.ID
	_, after, err := t.SetBillingTrust(r.Context(), store.BillingTrustChange{
		Trust: trust, Actor: actor, Reason: req.Reason, LimitCents: req.LimitCents,
		DailyCapCents: req.DailyCapCents, RungCents: req.RungCents, RunnerCap: req.RunnerCap,
	}, time.Now())
	switch {
	case errors.Is(err, store.ErrInvalidInput):
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	case err != nil:
		return err
	}
	writeJSON(w, http.StatusOK, billingTrustBody(t.Team(), after))
	return nil
}
