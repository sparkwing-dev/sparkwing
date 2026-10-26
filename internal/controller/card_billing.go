package controller

import (
	"context"
	"errors"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/mailer"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// billingInterval is how often the payment worker opens due refills, makes their Stripe calls and makes queued refunds.
const billingInterval = 30 * time.Second

// Machine-readable codes on a refused billing request.
const (
	noCardCode = "no_card"
)

const refillCreated = "created"

type hostedPageJSON struct {
	URL string `json:"url"`
}

func (s *Server) handleTeamBillingCard(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if err := emptyBillingBody(r); err != nil {
		return err
	}
	return s.openBillingPage(w, r, a, capability.BillingPageCard, 0)
}

func (s *Server) handleTeamBillingPortal(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if err := emptyBillingBody(r); err != nil {
		return err
	}
	if s.checkout == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "this controller opens no payment pages", Details: map[string]any{"reason": checkoutUnavailableCode}}
	}
	t, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	spend, err := t.SpendStanding(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	if spend.Card.Customer == "" {
		return &apierr.Error{Code: apierr.Conflict, Message: store.ErrNoCard.Error(), Details: map[string]any{"reason": noCardCode}}
	}
	url, err := s.checkout.portal(r.Context(), s.paymentDeployment, a.Target.Team, spend.Card.Customer)
	if err != nil {
		return errors.Join(&apierr.Error{Code: apierr.Unavailable, Message: "the card page could not be opened; try again"}, err)
	}
	writeJSON(w, http.StatusOK, hostedPageJSON{URL: url})
	return nil
}

type teamRefillReq struct {
	Enabled         bool  `json:"enabled"`
	ThresholdCents  int64 `json:"threshold_cents"`
	AmountCents     int64 `json:"amount_cents"`
	WeekLimitCents  int64 `json:"week_limit_cents"`
	MonthLimitCents int64 `json:"month_limit_cents"`
}

func (s *Server) handleTeamBillingRefill(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var req teamRefillReq
	if err := decodeEngineRequest(r, &req); err != nil {
		return err
	}
	writer, err := s.billingWriter(r, a)
	if err != nil {
		return err
	}
	if err := writer.SetAuthorizedRefill(r.Context(), a, store.RefillSettings(req)); err != nil {
		return billingMutationError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// safety: operator alerting keys on this value, so renaming it silences the alert for a refused card payment.
const cardPaymentRefusedAlert = "card_payment_refused"

// safety: every verified payment outcome reports through here, so a refused payment alerts the operator and a failed
// refill reaches the owners whichever event settled it.
func (s *Server) reportPaymentSettlement(ctx context.Context, settled store.PaymentSettlement) {
	attrs := []any{"team", string(settled.Team), "payment_intent", settled.Payment}
	switch {
	case settled.Replayed:
	case settled.Refused != "":
		s.logger.Warn("billing alert: a payment was refused and queued for refund",
			append(attrs, "alert", cardPaymentRefusedAlert, "reason", settled.Refused)...)
		if settled.RefillFailed != "" {
			s.mailOwners(ctx, settled.Team, "Your Sparkwing automatic refill was refused",
				"We queued a full refund for an automatic refill because the card issuer reported possible fraud on it ("+settled.Refused+"). "+
					"This refill did not add credits, and Cloud work is held; see the billing page:", s.billingPageURL())
		}
	case settled.RefillFailed != "":
		s.logger.Warn("billing alert: an automatic refill failed", append(attrs, "alert", "refill_failed", "decline_code", settled.RefillFailed)...)
		s.mailOwners(ctx, settled.Team, "Your Sparkwing automatic refill failed",
			"We could not charge your card for an automatic refill ("+settled.RefillFailed+"). "+
				"Update your card with Manage card on the billing page; refills stop until Add funds succeeds, and Cloud work stops when the credits run out:", s.billingPageURL())
	case settled.Granted:
		s.logger.Info("payment granted", append(attrs, "grant", settled.GrantID, "amount_micro", settled.AmountMicro)...)
	}
}

func (s *Server) runBilling(ctx context.Context, every time.Duration) {
	if s.checkout == nil {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.refillPass(ctx)
		s.refundQueuedPayments(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// safety: a refill's request is fixed when it opens, so every retry sends Stripe the same one under its key. Its
// payment id is recorded before the confirm call charges the card, and only the signed webhook grants credit; a
// crash before the id is recorded leaves a refill the next pass retries. A recorded payment is reconciled first;
// charging still requires current permission, and cancellation retains the reservation record.
func (s *Server) refillPass(ctx context.Context) {
	if !s.deployment.Metering() {
		return
	}
	operator := s.operator
	due, err := operator.DueRefills(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("refill pass", "err", err)
		}
		return
	}
	for _, r := range due {
		if r.PaymentIntent == "" {
			res, err := s.checkout.refill(ctx, s.paymentDeployment, r, "")
			if errors.Is(err, errRefillNoCard) {
				s.recordRefillFailure(ctx, r, store.RefillNoCard)
				continue
			}
			if err != nil || res.Status != refillCreated {
				s.logger.Error("creating a refill payment failed; it is retried", "team", string(r.Team), "refill", r.ID, "status", res.Status, "err", err)
				continue
			}
			if err := operator.RecordRefillIntent(ctx, r.Team, r.ID, res.PaymentIntent); err != nil {
				s.logger.Error("recording a refill payment failed; it is retried", "team", string(r.Team), "refill", r.ID, "err", err)
				continue
			}
			r.PaymentIntent = res.PaymentIntent
		}
		res, err := s.checkout.refill(ctx, s.paymentDeployment, r, "reconcile")
		if err != nil {
			s.logger.Error("reconciling a refill payment failed; it is retried", "team", string(r.Team), "refill", r.ID, "err", err)
			continue
		}
		switch res.Status {
		case "requires_confirmation", "requires_payment_method", "requires_action", "canceled":
		default:
			continue
		}
		if res.Status != "canceled" && res.Status != "requires_action" {
			allowed, err := operator.CanConfirmRefill(ctx, r)
			if err != nil {
				s.logger.Error("checking refill confirmation failed; it is retried", "team", string(r.Team), "refill", r.ID, "err", err)
				continue
			}
			action := "cancel"
			if allowed && res.Status == "requires_confirmation" {
				action = "confirm"
			} else if allowed {
				continue
			}
			res, err = s.checkout.refill(ctx, s.paymentDeployment, r, action)
			if err != nil {
				s.logger.Error("updating a refill payment failed; it is retried", "team", string(r.Team), "refill", r.ID, "action", action, "err", err)
				continue
			}
		}
		if res.Status == "requires_action" {
			s.recordRefillFailure(ctx, r, store.RefillAuthenticationRequired)
		}
		if res.Status == "canceled" {
			if err := operator.RecordRefillCanceled(ctx, r); err != nil {
				s.logger.Error("recording a canceled refill failed; it is retried", "team", string(r.Team), "refill", r.ID, "err", err)
			}
		}
	}
}

// recordRefillFailure stops refills the way a declined payment does and notifies the owners; a failed write is retried
// on the next pass.
func (s *Server) recordRefillFailure(ctx context.Context, r store.Refill, code string) {
	settled, err := s.operator.RecordRefillFailed(ctx, r, code)
	if err != nil {
		s.logger.Error("recording a refill failure; it is retried", "team", string(r.Team), "refill", r.ID, "failure", code, "err", err)
		return
	}
	s.reportPaymentSettlement(ctx, settled)
}

// safety: a refused payment is refunded under a key naming its retry, so a
// repeated pass repeats the same refund and a retry after a failed one makes a
// new one; the refund's success reverses any grant.
func (s *Server) refundQueuedPayments(ctx context.Context) {
	operator := s.operator
	due, err := operator.DueCardRefunds(ctx)
	if err != nil {
		s.logger.Error("listing queued card refunds", "err", err)
		return
	}
	for _, d := range due {
		refund, status, err := s.checkout.refund(ctx, d.PaymentIntent, d.Key, d.Reason)
		if err != nil {
			s.logger.Error("refunding a queued card payment failed; it is retried", "team", string(d.Team),
				"payment_intent", d.PaymentIntent, "err", err)
			continue
		}
		if err := operator.MarkCardRefundMade(ctx, d.Team, d.Key, refund); err != nil {
			s.logger.Error("recording a card refund failed", "payment_intent", d.PaymentIntent, "err", err)
			continue
		}
		s.logger.Warn("billing alert: a queued card refund was submitted", "alert", "card_refund",
			"team", string(d.Team), "payment_intent", d.PaymentIntent, "refund", refund, "status", status,
			"reason", d.Reason)
	}
}

// A failed send loses only the email; the committed state remains visible.
func (s *Server) mailOwners(ctx context.Context, team store.Team, subject, text, link string) {
	t, err := s.tenantForTeam(ctx, team)
	if err != nil {
		s.logger.Warn("owner mail: team", "team", string(team), "err", err)
		return
	}
	owners, err := t.TeamOwnerEmails(ctx)
	if err != nil {
		s.logger.Warn("owner mail: owners", "team", string(team), "err", err)
		return
	}
	s.mailRecipients(ctx, owners, subject, text, link)
}

func (s *Server) mailRecipients(ctx context.Context, recipients []string, subject, text, link string) {
	m := s.mailer
	if m == nil {
		m = mailer.Log{Logger: s.logger}
	}
	for _, to := range recipients {
		msg := mailer.Message{
			To: to, Subject: subject, Text: text + "\n" + link + "\n",
			HTML: "<p>" + html.EscapeString(text) + "</p><p><a href=\"" + html.EscapeString(link) + "\">" +
				html.EscapeString(link) + "</a></p>",
		}
		if err := m.Send(ctx, msg); err != nil {
			s.logger.Warn("owner mail: send", "err", err)
		}
	}
}

func (s *Server) billingPageURL() string {
	return strings.TrimRight(s.dashboardURL, "/") + "/team/billing"
}
