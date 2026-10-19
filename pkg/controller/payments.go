package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// PaymentSettler commits verified provider outcomes against stored billing records and resolves event ownership.
type PaymentSettler interface {
	capability.PaymentRecords
	SettlePayment(context.Context, capability.VerifiedPaymentEvent) (store.PaymentSettlement, error)
}

// WithPaymentDeployment names this deployment on every Stripe page and payment it opens, so it settles only its own events.
func (s *Server) WithPaymentDeployment(name string) *Server {
	s.deployment = strings.ToLower(strings.TrimSpace(name))
	return s
}

// WithPayments wires the provider verifier and the store's atomic settlement boundary.
// Configure it before constructing Handler. Missing dependencies refuse callbacks.
func (s *Server) WithPayments(verifier capability.PaymentAuthenticator, ledger PaymentSettler) *Server {
	s.paymentVerifier, s.paymentLedger = verifier, ledger
	return s
}

type paymentProofKey struct{}

type paymentProof struct {
	event *capability.VerifiedPaymentEvent
}

func (s *Server) paymentHandler() http.Handler {
	router := NewPolicyRouter(RouteAuthorization{})
	if err := router.Register(Endpoint{
		Method: http.MethodPost, Pattern: "/webhooks/stripe", Authentication: capability.AuthPaymentWebhook,
		Verify: s.verifyPaymentRequest, Handle: s.settlePaymentRequest,
	}); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writePolicyError(w, err) })
	}
	return router
}

func (s *Server) verifyPaymentRequest(r *http.Request) error {
	if s.paymentVerifier == nil || s.paymentLedger == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "payment settlement is not configured"}
	}
	signatures := r.Header.Values("Stripe-Signature")
	if len(signatures) != 1 || signatures[0] == "" {
		return &apierr.Error{Code: apierr.Unauthenticated, Message: "payment signature required"}
	}
	if r.Body == nil {
		return &apierr.Error{Code: apierr.BadRequest, Message: "payment event required"}
	}
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxJSONBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &apierr.Error{Code: apierr.PayloadTooLarge, Message: "payment event exceeds the JSON body limit"}
		}
		return &apierr.Error{Code: apierr.BadRequest, Message: "payment event could not be read"}
	}
	event, err := s.paymentVerifier.VerifyPaymentEvent(r.Context(), capability.PaymentPresentation{Body: body, Signature: signatures[0]}, capability.PaymentScope{Deployment: s.deployment, Records: s.paymentLedger})
	var conflict *capability.PaymentOwnershipConflict
	if errors.As(err, &conflict) {
		s.logger.Error("stripe event names another deployment for an object recorded here; not settled, Stripe retries", "alert", "payment_ownership_conflict", "object", conflict.EventObject, "stamp", conflict.Stamp, "deployment", s.deployment)
		return &apierr.Error{Code: apierr.Internal, Message: "payment event ownership conflicts with committed records"}
	}
	if err != nil {
		return err
	}
	// safety: proof belongs to this request, including a verified event that needs no settlement.
	*r = *r.WithContext(context.WithValue(r.Context(), paymentProofKey{}, paymentProof{event: event}))
	return nil
}

func (s *Server) settlePaymentRequest(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	proof, ok := r.Context().Value(paymentProofKey{}).(paymentProof)
	if !ok {
		return missingEndpointPolicy()
	}
	if proof.event == nil {
		s.logger.Debug("stripe event acknowledged without settlement")
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	if _, err := s.paymentLedger.SettlePayment(r.Context(), *proof.event); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

var _ PaymentSettler = (*store.Store)(nil)
