package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// PaymentSettler commits verified provider outcomes against stored billing records and resolves event ownership.
type PaymentSettler interface {
	capability.PaymentRecords
	SettlePayment(context.Context, capability.VerifiedPaymentEvent) (store.PaymentSettlement, error)
}

// WithPaymentDeployment names this deployment on every Stripe page and payment it opens, so it settles only its own events.
func (s *Server) WithPaymentDeployment(name string) *Server {
	s.paymentDeployment = strings.ToLower(strings.TrimSpace(name))
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
	event, err := s.paymentVerifier.VerifyPaymentEvent(r.Context(), capability.PaymentPresentation{Body: body, Signature: signatures[0]}, capability.PaymentScope{Deployment: s.paymentDeployment, Records: s.paymentLedger})
	var conflict *apierr.Error
	if errors.As(err, &conflict) && conflict.Details["reason"] == capability.PaymentOwnershipReason {
		s.logger.Error("stripe event names another deployment for an object recorded here; not settled, Stripe retries", "alert", "payment_ownership_conflict", "object", conflict.Details["object"], "stamp", conflict.Details["stamp"], "deployment", s.paymentDeployment)
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
	settled, err := s.paymentLedger.SettlePayment(r.Context(), *proof.event)
	if err != nil {
		return err
	}
	s.reportPaymentSettlement(r.Context(), settled)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

var _ PaymentSettler = (*store.Operator)(nil)

// ReconcilePayments pulls provider-owned facts through the same settlement transaction
// as webhooks. Restore must complete this sweep before acknowledging recovered state.
func (s *Server) ReconcilePayments(ctx context.Context) error {
	verifier, deployment, ledger := s.paymentVerifier, s.paymentDeployment, s.paymentLedger
	puller, ok := verifier.(capability.PaymentPuller)
	if !ok || ledger == nil || strings.TrimSpace(deployment) == "" {
		return &apierr.Error{Code: apierr.Unavailable, Message: "payment reconciliation is not configured"}
	}
	return puller.PullPayments(ctx, capability.PaymentScope{Deployment: strings.ToLower(strings.TrimSpace(deployment)), Records: ledger}, func(proof capability.VerifiedPaymentEvent) error {
		event := proof.Event()
		recorded, err := ledger.RecordsPaymentParent(ctx, capability.PaymentRoute{Mode: event.Mode, Kind: event.Kind, ObjectID: event.ObjectID, CheckoutID: event.CheckoutID, PaymentID: event.PaymentID})
		if err != nil {
			return err
		}
		if recorded == capability.PaymentParentPurged {
			return nil
		}
		if recorded != capability.PaymentParentRecorded {
			return &apierr.Error{Code: apierr.Conflict, Message: "provider payment has no restored team correlation; repair its reservation before acknowledging restore"}
		}
		settled, err := ledger.SettlePayment(ctx, proof)
		if err != nil {
			return err
		}
		s.reportPaymentSettlement(ctx, settled)
		return nil
	})
}

func (s *Server) runPaymentReconciliation(ctx context.Context) {
	if s.paymentVerifier == nil || s.paymentLedger == nil {
		return
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := s.ReconcilePayments(ctx); err != nil && ctx.Err() == nil {
			s.logger.Error("payment provider reconciliation failed; next sweep retries", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
