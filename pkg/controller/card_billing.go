package controller

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

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

// safety: only a trusted team adds a card, because the card is what lets its
// balance run below zero; an untrusted team keeps prepaying.
func (s *Server) handleTeamBillingCard(w http.ResponseWriter, r *http.Request) {
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	if s.checkout == nil {
		writeJSON(w, http.StatusServiceUnavailable, codedErrorJSON{
			Error: "this controller bills no cards", Code: CheckoutUnavailableCode,
		})
		return
	}
	ctx := r.Context()
	spend, err := t.SpendStanding(ctx, time.Now())
	if err != nil {
		s.writeInternalError(w, r, "team card standing", err)
		return
	}
	if !spend.Billing.Trusted {
		writeJSON(w, http.StatusConflict, codedErrorJSON{
			Error: "a card is added once the team is trusted; until then it prepays", Code: CardNotTrustedCode,
		})
		return
	}
	email := ""
	if p.AccountID != "" {
		if email, err = s.store.AccountEmail(ctx, p.AccountID); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.writeInternalError(w, r, "team card email", err)
			return
		}
	}
	page, err := s.checkout.hostedPage(ctx, "/internal/card-setup", map[string]any{
		"team": string(t.Team()), "email": email, "customer": spend.Card.Customer,
	})
	if err != nil {
		s.logger.Error("opening a card setup page failed", "team", string(t.Team()), "err", err)
		writeError(w, http.StatusBadGateway, errors.New("the card page could not be opened; try again"))
		return
	}
	writeJSON(w, http.StatusOK, hostedPageJSON{URL: page})
}

func (s *Server) handleTeamBillingPay(w http.ResponseWriter, r *http.Request) {
	_, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	if s.checkout == nil {
		writeJSON(w, http.StatusServiceUnavailable, codedErrorJSON{
			Error: "this controller bills no cards", Code: CheckoutUnavailableCode,
		})
		return
	}
	ctx := r.Context()
	work, err := t.StartRecoveryAttempt(ctx, time.Now())
	if errors.Is(err, store.ErrNoPayableCharge) {
		writeJSON(w, http.StatusConflict, codedErrorJSON{Error: err.Error(), Code: NoPayableChargeCode})
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "team card recovery", err)
		return
	}
	page, err := s.checkout.hostedPage(ctx, "/internal/charge-checkout", map[string]any{
		"team": string(work.Team), "charge_id": work.ChargeID, "attempt_id": work.AttemptID,
		"amount_cents": work.AmountCents, "customer": work.Customer,
	})
	if err != nil {
		if failErr := s.store.DropCardAttempt(ctx, work.AttemptID, time.Now()); failErr != nil {
			s.logger.Warn("closing an unopened recovery attempt failed", "attempt", work.AttemptID, "err", failErr)
		}
		s.logger.Error("opening a recovery checkout failed", "team", string(t.Team()), "err", err)
		writeError(w, http.StatusBadGateway, errors.New("the payment page could not be opened; try again"))
		return
	}
	writeJSON(w, http.StatusOK, hostedPageJSON{URL: page})
}

type teamBudgetReq struct {
	BudgetCents int64 `json:"budget_cents"`
}

func (s *Server) handleTeamBillingBudget(w http.ResponseWriter, r *http.Request) {
	_, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	var req teamBudgetReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch err := t.SetSpendBudget(r.Context(), req.BudgetCents); {
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
	case err != nil:
		s.writeInternalError(w, r, "team budget", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type savedCardReq struct {
	Team          string `json:"team"`
	Customer      string `json:"customer"`
	PaymentMethod string `json:"payment_method"`
	Fingerprint   string `json:"fingerprint"`
	Brand         string `json:"brand"`
	Last4         string `json:"last4"`
}

// safety: only the checkout service holds credits.grant, and it names the
// team the Stripe session was opened for by the controller.
func (s *Server) handleSavedCard(w http.ResponseWriter, r *http.Request) {
	var req savedCardReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	t, ok := s.namedTenant(w, r, req.Team)
	if !ok {
		return
	}
	err := t.SaveCard(r.Context(), store.Card{
		Customer: req.Customer, PaymentMethod: req.PaymentMethod, Fingerprint: req.Fingerprint,
		Brand: req.Brand, Last4: req.Last4,
	}, principalName(r), time.Now())
	switch {
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
	case err != nil:
		s.writeInternalError(w, r, "save card", err)
	default:
		s.logger.Info("card saved", "team", req.Team, "brand", req.Brand, "last4", req.Last4)
		w.WriteHeader(http.StatusNoContent)
	}
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

// safety: the webhook and the recovery Checkout report here, and so does the
// worker in-process; every path settles through the same store call, which
// is idempotent on the payment intent.
func (s *Server) handleCardPayment(w http.ResponseWriter, r *http.Request) {
	var req cardPaymentReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	err := s.applyCardResult(r.Context(), req)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
	case err != nil:
		s.writeInternalError(w, r, "card payment", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) applyCardResult(ctx context.Context, req cardPaymentReq) error {
	now := time.Now()
	switch req.Status {
	case cardChargeSucceeded:
		created, err := s.store.SettleCardPayment(ctx, store.CardPayment{
			Team: store.Team(req.Team), ChargeID: req.ChargeID, AttemptID: req.AttemptID,
			PaymentIntent: req.PaymentIntent, AmountCents: req.AmountCents, Fingerprint: req.Fingerprint,
		}, now)
		if created {
			s.logger.Info("card charge paid", "team", req.Team, "charge", req.ChargeID,
				"payment_intent", req.PaymentIntent, "cents", req.AmountCents)
		}
		return err
	case cardChargeFailed:
		if req.AttemptID == "" {
			return fmt.Errorf("%w: a failed payment names its attempt", store.ErrInvalidInput)
		}
		team, first, err := s.store.FailCardAttempt(ctx, req.AttemptID, req.PaymentIntent, req.DeclineCode, now)
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

type paymentWarningReq struct {
	PaymentIntent string `json:"payment_intent"`
	WarningID     string `json:"warning_id"`
	Fingerprint   string `json:"fingerprint"`
	Actionable    bool   `json:"actionable"`
	Reason        string `json:"reason"`
}

type paymentWarningJSON struct {
	Team string `json:"team"`
	Held bool   `json:"held"`
}

// safety: the warning is stored before anything else answers, so a payment
// that settles after it is never granted; only the checkout service holds
// credits.grant.
func (s *Server) handlePaymentWarning(w http.ResponseWriter, r *http.Request) {
	var req paymentWarningReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	team, err := s.store.RecordPaymentWarning(r.Context(), store.PaymentWarning{
		WarningID: req.WarningID, PaymentIntent: req.PaymentIntent, Fingerprint: req.Fingerprint,
		Actionable: req.Actionable,
	}, time.Now())
	switch {
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
		return
	case err != nil:
		s.writeInternalError(w, r, "payment warning", err)
		return
	}
	s.logger.Warn("billing alert: an early fraud warning was recorded", "alert", "early_fraud_warning",
		"warning", req.WarningID, "payment_intent", req.PaymentIntent, "actionable", req.Actionable,
		"team", string(team))
	writeJSON(w, http.StatusOK, paymentWarningJSON{Team: string(team), Held: team != ""})
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
	work, alerts, err := s.store.DueCardCharges(ctx, time.Now())
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
		if err := s.store.RecordAttemptIntent(ctx, wk.AttemptID, res.PaymentIntent, time.Now()); err != nil {
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
	due, err := s.store.DueCardRefunds(ctx, time.Now())
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
		if err := s.store.MarkCardRefundMade(ctx, d.Team, d.Key, refund, status, time.Now()); err != nil {
			s.logger.Error("recording a card refund failed", "payment_intent", d.PaymentIntent, "err", err)
			continue
		}
		s.logger.Warn("billing alert: a card payment that paid no charge was refunded", "alert", "card_refund",
			"team", string(d.Team), "payment_intent", d.PaymentIntent, "refund", refund, "status", status,
			"reason", d.Reason)
	}
}

type cardRefundReq struct {
	PaymentIntent string `json:"payment_intent"`
	RefundID      string `json:"refund_id"`
	Queue         string `json:"queue"`
	Status        string `json:"status"`
}

type cardRefundJSON struct {
	Queued bool `json:"queued"`
}

// safety: a refund the controller queued is done only when Stripe says it
// succeeded; a failed or canceled one is due again, so the payer is never
// left with neither the money nor the credits.
func (s *Server) handleCardRefund(w http.ResponseWriter, r *http.Request) {
	var req cardRefundReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.PaymentIntent == "" || req.RefundID == "" || req.Queue == "" || req.Status == "" {
		writeError(w, http.StatusBadRequest, errors.New("payment_intent, refund_id, queue and status are required"))
		return
	}
	queued, err := s.store.ReportCardRefund(r.Context(), req.Queue, req.RefundID, req.Status, time.Now())
	if err != nil {
		s.writeInternalError(w, r, "card refund", err)
		return
	}
	if queued && (req.Status == "failed" || req.Status == "canceled") {
		s.logger.Warn("billing alert: a queued card refund failed; it is retried", "alert", "card_refund_failed",
			"payment_intent", req.PaymentIntent, "refund", req.RefundID, "status", req.Status)
	}
	writeJSON(w, http.StatusOK, cardRefundJSON{Queued: queued})
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
