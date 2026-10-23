package controller

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// BillingWriter commits hosted-page reservations, refill settings and standing requests under current team authority.
type BillingWriter interface {
	Team() store.Team
	PrepareAuthorizedBillingPage(context.Context, capability.Authorized, capability.BillingPageKind, int64, string) (capability.BillingPage, error)
	CompleteAuthorizedBillingPage(context.Context, capability.Authorized, capability.BillingPage) error
	RecordAuthorizedBillingPageFailure(context.Context, capability.Authorized, capability.BillingPage) error
	SetAuthorizedRefill(context.Context, capability.Authorized, store.RefillSettings) error
	RequestAuthorizedStanding(context.Context, capability.Authorized, string, string) (store.StandingRequest, error)
}

var _ BillingWriter = (*store.Tenant)(nil)

func (s *Server) billingWriter(r *http.Request, a capability.Authorized) (BillingWriter, error) {
	return tenantPort[BillingWriter](r.Context(), s, store.Team(a.Target.Team))
}

func (s *Server) billingEndpoints() []controllerwire.Endpoint {
	return []controllerwire.Endpoint{
		{Method: "POST", Pattern: "/api/v1/team/billing/checkout", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.meteredOnly(s.handleTeamBillingCheckout)},
		{Method: "POST", Pattern: "/api/v1/team/billing/standing-request", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.meteredOnly(s.handleTeamStandingRequest)},
		{Method: "POST", Pattern: "/api/v1/team/billing/card", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.meteredOnly(s.handleTeamBillingCard)},
		{Method: "PUT", Pattern: "/api/v1/team/billing/refill", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.meteredOnly(s.handleTeamBillingRefill)},
	}
}

func (s *Server) openBillingPage(w http.ResponseWriter, r *http.Request, a capability.Authorized, kind capability.BillingPageKind, amount int64) error {
	if s.checkout == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "this controller opens no payment pages", Details: map[string]any{"reason": CheckoutUnavailableCode}}
	}
	key, err := controlIdempotencyKey(r)
	if err != nil {
		return err
	}
	writer, err := s.billingWriter(r, a)
	if err != nil {
		return err
	}
	page, err := writer.PrepareAuthorizedBillingPage(r.Context(), a, kind, amount, key)
	if err != nil {
		return billingMutationError(err)
	}
	if page.Kind != kind || page.Team != a.Target.Team || page.ReservationID == "" || kind == capability.BillingPageCredits && page.AmountCents != amount {
		return routepolicy.MissingEndpointPolicy()
	}
	if page.URL != "" && !validBillingPageURL(page.URL) {
		return routepolicy.MissingEndpointPolicy()
	}
	if page.URL == "" {
		if err := s.checkout.openReservedPage(r.Context(), s.paymentDeployment, &page); err != nil {
			recordErr := writer.RecordAuthorizedBillingPageFailure(r.Context(), a, page)
			return errors.Join(&apierr.Error{Code: apierr.Unavailable, Message: "the payment page could not be opened; try again"}, err, recordErr)
		}
		if err := writer.CompleteAuthorizedBillingPage(r.Context(), a, page); err != nil {
			return billingMutationError(err)
		}
	}
	writeJSON(w, http.StatusOK, hostedPageJSON{URL: page.URL})
	return nil
}

func billingMutationError(err error) error {
	switch {
	case errors.Is(err, store.ErrInvalidInput):
		return invalidEngineBody(err.Error())
	case errors.Is(err, store.ErrNoCard):
		return &apierr.Error{Code: apierr.Conflict, Message: err.Error(), Details: map[string]any{"reason": NoCardCode}}
	case moneyInFlight(err):
		return &apierr.Error{Code: apierr.Conflict, Message: err.Error()}
	default:
		return accountMutationError(err)
	}
}

func emptyBillingBody(r *http.Request) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		return invalidEngineBody("request takes no body")
	}
	return nil
}
