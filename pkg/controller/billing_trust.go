package controller

import (
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type billingTrustReq struct {
	Trust  string `json:"trust"`
	Reason string `json:"reason"`
}

type billingTrustJSON struct {
	Team          string `json:"team"`
	Trust         string `json:"trust"`
	Trusted       bool   `json:"trusted"`
	TrustBy       string `json:"trust_by,omitempty"`
	TrustAt       int64  `json:"trust_at,omitempty"`
	TrustReason   string `json:"trust_reason,omitempty"`
	Standing      string `json:"standing"`
	CloudVCPU     int64  `json:"cloud_vcpu"`
	MaxJobSeconds int64  `json:"max_job_seconds"`
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
		TrustReason: b.TrustReason, Standing: b.Tier, CloudVCPU: b.CloudVCPU, MaxJobSeconds: int64(b.MaxJobDuration.Seconds()),
	}
	if !b.TrustAt.IsZero() {
		out.TrustAt = b.TrustAt.Unix()
	}
	return out
}

func (s *Server) handleBillingTrustSet(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	writer, err := s.consoleWriter()
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
	after, err := writer.ConsoleSetBillingTrust(r.Context(), authorized, store.BillingTrustChange{
		Trust: trust, Actor: actor, Reason: req.Reason,
	})
	switch {
	case errors.Is(err, store.ErrInvalidInput):
		return &apierr.Error{Code: apierr.BadRequest, Message: err.Error()}
	case err != nil:
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, billingTrustBody(store.Team(authorized.Target.Team), after))
	return nil
}
