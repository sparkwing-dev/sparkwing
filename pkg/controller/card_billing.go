package controller

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/mailer"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CardBillingInterval is how often the payment worker opens due charges and
// makes their Stripe calls.
const CardBillingInterval = 30 * time.Second

// Machine-readable codes on a refused card billing request.
const (
	CardNotTrustedCode   = "card_not_trusted"
	CardOnFileCode       = "card_on_file"
	NoPayableChargeCode  = "no_payable_charge"
	PaymentWarnedCode    = "payment_warned"
	cardChargeSucceeded  = "succeeded"
	cardChargeFailed     = "failed"
	cardChargeProcessing = "processing"
	cardChargeCreated    = "created"
)

type hostedPageJSON struct {
	URL string `json:"url"`
}

func (s *Server) handleTeamBillingCard(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if err := emptyBillingBody(r); err != nil {
		return err
	}
	return s.openBillingPage(w, r, a, capability.BillingPageCard, 0)
}

func (s *Server) handleTeamBillingPay(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if err := emptyBillingBody(r); err != nil {
		return err
	}
	return s.openBillingPage(w, r, a, capability.BillingPageRecovery, 0)
}

type teamBudgetReq struct {
	BudgetCents int64 `json:"budget_cents"`
}

func (s *Server) handleTeamBillingBudget(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if !s.Metering() {
		return &apierr.Error{Code: apierr.NotFound, Message: "billing is unavailable"}
	}
	var req teamBudgetReq
	if err := decodeEngineRequest(r, &req); err != nil {
		return err
	}
	writer, err := s.billingWriter(r, a)
	if err != nil {
		return err
	}
	if err := writer.SetAuthorizedSpendBudget(r.Context(), a, req.BudgetCents); err != nil {
		return billingMutationError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type cardPaymentReq struct {
	Team          string `json:"team"`
	ChargeID      string `json:"charge_id"`
	AttemptID     string `json:"attempt_id"`
	PaymentIntent string `json:"payment_intent"`
	AmountCents   int64  `json:"amount_cents"`
	Status        string `json:"status"`
	DeclineCode   string `json:"decline_code"`
	Fingerprint   string `json:"fingerprint"`
}

// safety: operator alerting keys on this value, so renaming it silences the
// alert for a fraud-warned card's payment.
const cardPaymentWarnedAlert = "card_payment_warned"

func (s *Server) applyCardResult(ctx context.Context, req cardPaymentReq) error {
	now := time.Now()
	switch req.Status {
	case cardChargeSucceeded:
		created, warned, err := s.store.AsOperator().SettleCardPayment(ctx, store.CardPayment{
			Team: store.Team(req.Team), ChargeID: req.ChargeID, AttemptID: req.AttemptID,
			PaymentIntent: req.PaymentIntent, AmountCents: req.AmountCents, Fingerprint: req.Fingerprint,
		}, now)
		switch {
		case warned != nil && warned.Repays:
			s.logger.Error("billing alert: a card with a fraud warning repaid a debt; the team is held and any unapplied rest is the operator's to return",
				"alert", cardPaymentWarnedAlert, "team", req.Team, "charge", req.ChargeID,
				"payment_intent", req.PaymentIntent, "warning_id", warned.WarningID, "cents", req.AmountCents,
				"repaid_micro", warned.RepaidMicro, "unapplied_micro", warned.UnappliedMicro)
		case warned != nil:
			s.logger.Error("billing alert: a payment by a card with a fraud warning granted nothing; confirm it was refunded",
				"alert", cardPaymentWarnedAlert, "team", req.Team, "charge", req.ChargeID,
				"payment_intent", req.PaymentIntent, "warning_id", warned.WarningID, "cents", req.AmountCents,
				"unapplied_micro", warned.UnappliedMicro)
		case created:
			s.logger.Info("card charge paid", "team", req.Team, "charge", req.ChargeID,
				"payment_intent", req.PaymentIntent, "cents", req.AmountCents)
		}
		return err
	case cardChargeFailed:
		if req.AttemptID == "" {
			return fmt.Errorf("%w: a failed payment names its attempt", store.ErrInvalidInput)
		}
		team, first, err := s.store.AsOperator().FailCardAttempt(ctx, req.AttemptID, req.PaymentIntent, req.DeclineCode, now)
		if err != nil {
			return err
		}
		s.logger.Warn("billing alert: a card charge failed", "alert", "card_charge_failed", "team", string(team),
			"attempt", req.AttemptID, "decline_code", req.DeclineCode)
		if first {
			s.mailOwners(ctx, team, "Your Sparkwing card payment failed",
				"We could not charge your card for Sparkwing Cloud usage ("+req.DeclineCode+"). "+
					"New Cloud work waits until it is paid. Pay now or update the card on the billing page:")
		}
		return nil
	case cardChargeProcessing, cardChargeCreated:
		return nil
	}
	return fmt.Errorf("%w: status must be succeeded, failed or processing", store.ErrInvalidInput)
}

func (s *Server) runCardBilling(ctx context.Context, every time.Duration) {
	if s.checkout == nil {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.cardBillingPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// safety: the Stripe call runs outside the ledger lock, under the attempt the
// store already committed; a crash between them leaves that attempt live,
// and the next pass asks Stripe for it by the same id instead of charging
// again.
func (s *Server) cardBillingPass(ctx context.Context) {
	work, alerts, err := s.store.AsOperator().DueCardCharges(ctx, time.Now())
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("card billing pass", "err", err)
		}
		return
	}
	for _, a := range alerts {
		s.logger.Warn("billing alert: a team crossed its budget threshold", "alert", "budget_threshold",
			"team", string(a.Team), "percent", a.Percent)
		s.mailOwners(ctx, a.Team, fmt.Sprintf("Sparkwing Cloud: %d%% of your budget used", a.Percent),
			fmt.Sprintf("Your team has spent $%.2f of its $%.2f budget over the last 30 days. "+
				"At 100%% new Cloud work waits. Review the budget on the billing page:",
				float64(a.Spent30dMicro)/float64(store.MicroCreditsPerCent*100),
				float64(a.BudgetMicro)/float64(store.MicroCreditsPerCent*100)))
	}
	for _, wk := range work {
		res, err := s.chargeCard(ctx, wk)
		if err != nil {
			s.logger.Error("card charge call failed; the attempt is retried", "team", string(wk.Team),
				"charge", wk.ChargeID, "attempt", wk.AttemptID, "err", err)
			continue
		}
		if err := s.applyCardResult(ctx, cardPaymentReq{
			Team: string(wk.Team), ChargeID: wk.ChargeID, AttemptID: wk.AttemptID,
			PaymentIntent: res.PaymentIntent, AmountCents: wk.AmountCents,
			Status: res.Status, DeclineCode: res.DeclineCode, Fingerprint: res.Fingerprint,
		}); err != nil {
			s.logger.Error("recording a card charge failed", "team", string(wk.Team), "charge", wk.ChargeID,
				"attempt", wk.AttemptID, "payment_intent", res.PaymentIntent, "err", err)
		}
	}
	s.refundQueuedPayments(ctx)
}

// safety: the payment is created unconfirmed and its id recorded before it
// is confirmed, so a crash anywhere leaves an attempt the next pass finishes
// by that id; no search of the customer's payments is needed.
func (s *Server) chargeCard(ctx context.Context, wk store.CardChargeWork) (cardChargeResult, error) {
	if wk.PaymentIntent == "" {
		res, err := s.checkout.charge(ctx, wk)
		if err != nil || res.Status != cardChargeCreated {
			return res, err
		}
		if err := s.store.AsOperator().RecordAttemptIntent(ctx, wk.AttemptID, res.PaymentIntent, wk.Customer, time.Now()); err != nil {
			return res, err
		}
		wk.PaymentIntent = res.PaymentIntent
	}
	return s.checkout.charge(ctx, wk)
}

// safety: a payment that paid no charge is refunded under a key naming its
// retry, so a repeated pass repeats the same refund and a retry after a
// failed one makes a new one; the refund's success reverses its grant.
func (s *Server) refundQueuedPayments(ctx context.Context) {
	due, err := s.store.AsOperator().DueCardRefunds(ctx, time.Now())
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
		if err := s.store.AsOperator().MarkCardRefundMade(ctx, d.Team, d.Key, refund, status, time.Now()); err != nil {
			s.logger.Error("recording a card refund failed", "payment_intent", d.PaymentIntent, "err", err)
			continue
		}
		s.logger.Warn("billing alert: a card payment that paid no charge was refunded", "alert", "card_refund",
			"team", string(d.Team), "payment_intent", d.PaymentIntent, "refund", refund, "status", status,
			"reason", d.Reason)
	}
}

// safety: a failed send loses only the email; the dashboard shows the same
// state, so billing never waits on the mail service.
func (s *Server) mailOwners(ctx context.Context, team store.Team, subject, text string) {
	t, err := s.store.ForTeam(ctx, team)
	if err != nil {
		s.logger.Warn("billing mail: team", "team", string(team), "err", err)
		return
	}
	owners, err := t.TeamOwnerEmails(ctx)
	if err != nil {
		s.logger.Warn("billing mail: owners", "team", string(team), "err", err)
		return
	}
	link := s.billingPageURL()
	m := s.mailer
	if m == nil {
		m = mailer.Log{Logger: s.logger}
	}
	for _, to := range owners {
		msg := mailer.Message{
			To: to, Subject: subject, Text: text + "\n" + link + "\n",
			HTML: "<p>" + html.EscapeString(text) + "</p><p><a href=\"" + html.EscapeString(link) + "\">" +
				html.EscapeString(link) + "</a></p>",
		}
		if err := m.Send(ctx, msg); err != nil {
			s.logger.Warn("billing mail: send", "team", string(team), "err", err)
		}
	}
}

func (s *Server) billingPageURL() string {
	return strings.TrimRight(s.dashboardURL, "/") + "/team/billing"
}
