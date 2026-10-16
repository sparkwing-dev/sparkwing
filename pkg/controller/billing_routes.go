package controller

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// BillingWriter commits hosted-page reservations and budgets under current team authority.
type BillingWriter interface {
	Team() store.Team
	PrepareAuthorizedBillingPage(context.Context, capability.Authorized, capability.BillingPageKind, int64, string) (capability.BillingPage, error)
	CompleteAuthorizedBillingPage(context.Context, capability.Authorized, capability.BillingPage) error
	RecordAuthorizedBillingPageFailure(context.Context, capability.Authorized, capability.BillingPage) error
	SetAuthorizedSpendBudget(context.Context, capability.Authorized, int64) error
}

// WithBillingWriter supplies tenant billing transactions before Handler is constructed.
func (s *Server) WithBillingWriter(resolve func(context.Context, store.Team) (BillingWriter, error)) *Server {
	s.resolveBillingWriter = resolve
	return s
}

func (s *Server) billingWriter(r *http.Request, a capability.Authorized) (BillingWriter, error) {
	if a.Target.Team == "" {
		return nil, missingEndpointPolicy()
	}
	var writer BillingWriter
	var err error
	if s.resolveBillingWriter != nil {
		writer, err = s.resolveBillingWriter(r.Context(), store.Team(a.Target.Team))
	} else if s.store != nil {
		var tenant *store.Tenant
		tenant, err = s.reportingTenant(r, a)
		writer, _ = any(tenant).(BillingWriter)
	}
	if err != nil {
		return nil, engineLookupError(err)
	}
	if writer == nil || writer.Team() != store.Team(a.Target.Team) {
		return nil, missingEndpointPolicy()
	}
	return writer, nil
}

func (s *Server) billingEndpoints() []Endpoint {
	return []Endpoint{
		{Method: "POST", Pattern: "/api/v1/team/billing/checkout", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleTeamBillingCheckout},
		{Method: "POST", Pattern: "/api/v1/team/billing/card", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleTeamBillingCard},
		{Method: "POST", Pattern: "/api/v1/team/billing/pay", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleTeamBillingPay},
		{Method: "PUT", Pattern: "/api/v1/team/billing/budget", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleTeamBillingBudget},
	}
}

func (s *Server) openBillingPage(w http.ResponseWriter, r *http.Request, a capability.Authorized, kind capability.BillingPageKind, amount int64) error {
	if !s.Metering() {
		return &apierr.Error{Code: apierr.NotFound, Message: "billing is unavailable"}
	}
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
		return missingEndpointPolicy()
	}
	if page.URL != "" && !validBillingPageURL(page.URL) {
		return missingEndpointPolicy()
	}
	if page.URL == "" {
		if err := s.checkout.openReservedPage(r.Context(), &page); err != nil {
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
	case errors.Is(err, store.ErrNoPayableCharge):
		return &apierr.Error{Code: apierr.Conflict, Message: err.Error(), Details: map[string]any{"reason": NoPayableChargeCode}}
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
