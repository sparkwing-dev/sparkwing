package store

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// SetPaymentMode configures the provider mode. Switching modes requires a store without ledger activity.
func (o *Operator) SetPaymentMode(ctx context.Context, mode capability.PaymentMode) error {
	if mode != capability.PaymentTest && mode != capability.PaymentLive {
		return ErrInvalidInput
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		if _, err := tx.execContext(ctx, `INSERT INTO settings(key,value) VALUES ('payment_mode',?) ON CONFLICT(key) DO NOTHING`, []byte(`"test"`)); err != nil {
			return err
		}
		var encoded []byte
		if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='payment_mode'`+tx.forUpdate()).Scan(&encoded); err != nil {
			return err
		}
		var current capability.PaymentMode
		if err := json.Unmarshal(encoded, &current); err != nil {
			return err
		}
		if current == mode {
			return nil
		}
		var activity bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM credit_grants)
            OR EXISTS(SELECT 1 FROM credit_charges) OR EXISTS(SELECT 1 FROM credit_checkouts)`).Scan(&activity); err != nil {
			return err
		}
		if activity {
			return authorityError(apierr.Conflict, "payment mode cannot change after ledger activity; start a fresh store to switch modes")
		}
		data, err := json.Marshal(mode)
		if err != nil {
			return err
		}
		_, err = tx.execContext(ctx, `UPDATE settings SET value=? WHERE key='payment_mode'`, data)
		return err
	})
}

// PaymentSettlement reports committed credit without accepting caller-selected credits.
type PaymentSettlement struct {
	Team        Team
	CheckoutID  string
	GrantID     string
	AmountMicro int64
	Replayed    bool
	// Payment is the provider payment the settlement concerns.
	Payment string
	// Granted reports that the payment funded a grant.
	Granted bool
	// Refused names why the payment was queued for a full refund, or is empty.
	Refused string
	// RefillFailed is the decline code of a refill that failed, which the owners are told to fix by updating the card.
	RefillFailed string
}

// SettlePayment settles verified provider events against committed billing facts.
func (o *Operator) SettlePayment(ctx context.Context, proof capability.VerifiedPaymentEvent) (PaymentSettlement, error) {
	if proof == (capability.VerifiedPaymentEvent{}) {
		return PaymentSettlement{}, authorityError(apierr.Unauthenticated, "payment verification is required")
	}
	return o.s.settlePaymentFacts(ctx, proof.Event())
}

type paymentCheckout struct {
	result                  PaymentSettlement
	kind, payment, currency string
	amount                  int64
	paid, closed            sql.NullInt64
	grant                   sql.NullString
}

func (c *paymentCheckout) scan() []any {
	return []any{&c.result.CheckoutID, &c.result.Team, &c.kind, &c.payment, &c.currency, &c.amount, &c.result.AmountMicro, &c.paid, &c.closed, &c.grant}
}

func paymentPaid(kind capability.PaymentEventKind) bool {
	return kind == capability.PaymentCheckoutPaid || kind == capability.PaymentCardPaid
}

func (op *Operator) applyCheckoutPaymentTx(ctx context.Context, tx *storeTx, event capability.PaymentEvent) (PaymentSettlement, error) {
	var checkout paymentCheckout
	err := tx.QueryRowContext(ctx, `SELECT id,team,kind,payment_id,currency,amount_minor,amount_micro,paid_at,closed_at,grant_id FROM credit_checkouts WHERE payment_mode = ? AND session_id = ? AND session_id <> id`+tx.forUpdate(), string(event.Mode), event.CheckoutID).Scan(checkout.scan()...)
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentSettlement{}, authorityError(apierr.NotFound, "committed checkout is unavailable")
	}
	if err != nil {
		return PaymentSettlement{}, err
	}
	if checkout.currency != event.Currency || checkout.amount != event.AmountMinor || (event.PaymentID != "" && checkout.payment != "" && checkout.payment != event.PaymentID) {
		return PaymentSettlement{}, authorityError(apierr.Conflict, "payment facts conflict with committed checkout")
	}
	if event.PaymentID != "" {
		var used int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM credit_checkouts WHERE payment_mode=? AND payment_id=? AND id<>?", string(event.Mode), event.PaymentID, checkout.result.CheckoutID).Scan(&used); err != nil {
			return PaymentSettlement{}, err
		}
		if used != 0 {
			return PaymentSettlement{}, errors.Join(ErrCreditGrantConflict, authorityError(apierr.Conflict, "payment identity belongs to another checkout"))
		}
		grant, found, err := tx.operator.paidGrantForPaymentTx(ctx, tx, event.PaymentID)
		if err != nil {
			return PaymentSettlement{}, err
		}
		if found && (grant.Team != checkout.result.Team || grant.ID != checkout.grant.String) {
			return PaymentSettlement{}, errors.Join(ErrCreditGrantConflict, authorityError(apierr.Conflict, "payment identity already funded another grant"))
		}
	}
	checkout.result.GrantID, checkout.result.Payment = checkout.grant.String, event.PaymentID
	checkout.result.Replayed = checkout.paid.Valid
	if err := applyPaymentOutcomeTx(ctx, tx, event, &checkout); err != nil {
		return PaymentSettlement{}, err
	}
	return checkout.result, nil
}

// safety: replay identity covers only what Stripe signed, so a retry whose provider reads changed since the first
// delivery is still the same event.
func paymentEventDigest(event capability.PaymentEvent) (string, error) {
	body, err := json.Marshal(event.Identity())
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func configuredPaymentModeTx(ctx context.Context, tx *storeTx, eventMode capability.PaymentMode) error {
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return err
	}
	if mode != eventMode {
		return authorityError(apierr.Forbidden, "payment mode does not match configuration")
	}
	return nil
}

func paymentModeTx(ctx context.Context, tx *storeTx) (capability.PaymentMode, error) {
	var encoded []byte
	if err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = 'payment_mode'"+tx.forShare()).Scan(&encoded); err != nil {
		return "", authorityError(apierr.Unavailable, "payment mode is not configured")
	}
	var mode capability.PaymentMode
	if json.Unmarshal(encoded, &mode) != nil || (mode != capability.PaymentTest && mode != capability.PaymentLive) {
		return "", authorityError(apierr.Unavailable, "configured payment mode is invalid")
	}
	return mode, nil
}

func paymentEventReplayTx(ctx context.Context, tx *storeTx, event capability.PaymentEvent, digest string) (bool, error) {
	var existing string
	err := tx.QueryRowContext(ctx, "SELECT event_digest FROM payment_events WHERE payment_mode = ? AND event_id = ?", string(event.Mode), event.EventID).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if existing != digest {
		return false, authorityError(apierr.Conflict, "payment event identity has conflicting content")
	}
	return true, nil
}

// safety: Add funds and refills settle through this one path, under the team's ledger lock: a refused card closes
// the row unpaid and queues its refund, a paid one funds one grant and takes back any refund that arrived first.
func applyPaymentOutcomeTx(ctx context.Context, tx *storeTx, event capability.PaymentEvent, checkout *paymentCheckout) error {
	team, now := checkout.result.Team, tx.now(ctx)
	l, err := lockLedgerTx(ctx, tx, team)
	if err != nil {
		return err
	}
	if checkout.payment == "" && event.PaymentID != "" {
		if _, err := tx.execContext(ctx, "UPDATE credit_checkouts SET payment_id = ? WHERE team = ? AND id = ? AND payment_mode = ?", event.PaymentID, string(team), checkout.result.CheckoutID, string(event.Mode)); err != nil {
			return err
		}
	}
	if checkout.paid.Valid {
		return nil
	}
	if !paymentPaid(event.Kind) {
		if checkout.closed.Valid {
			return nil
		}
		return closeUnpaidTx(ctx, tx, event, checkout)
	}
	// A paid outcome replaces any earlier failed outcome and starts its spending windows. Later deliveries
	// cannot change the protection facts or restart those windows.
	if _, err := tx.execContext(ctx, `UPDATE credit_checkouts SET three_d_secure = ?, liability_shift = ?, closed_at = ?
	    WHERE team = ? AND id = ? AND payment_mode = ? AND liability_shift IS NULL`, event.ThreeDS, boolInt(event.LiabilityShift), now.UnixNano(), string(team), checkout.result.CheckoutID, string(event.Mode)); err != nil {
		return err
	}
	var shifted int
	if err := tx.QueryRowContext(ctx, `SELECT three_d_secure,liability_shift FROM credit_checkouts WHERE team = ? AND id = ? AND payment_mode = ?`, string(team), checkout.result.CheckoutID, string(event.Mode)).Scan(&event.ThreeDS, &shifted); err != nil {
		return err
	}
	event.LiabilityShift = shifted == 1
	refusal, err := claimCardTx(ctx, tx, team, event.PaymentID, event.Fingerprint, now)
	if err != nil {
		return err
	}
	if refusal.warning != "" && event.LiabilityShift {
		var warnedPayment string
		if err := tx.QueryRowContext(ctx, `SELECT payment_intent FROM payment_warnings WHERE payment_mode=? AND warning_id = ? AND actionable=1`, event.Mode, refusal.warning).Scan(&warnedPayment); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// A warning can precede its paid event; protect that payment while keeping the team held.
		if warnedPayment == event.PaymentID {
			refusal, err = claimCardPinTx(ctx, tx, team, event.PaymentID, event.Fingerprint, now)
			if err != nil {
				return err
			}
		}
	}
	if refusal.reason != "" {
		return refuseCheckoutTx(ctx, tx, event, checkout, refusal)
	}
	id, err := newCreditID("paid")
	if err != nil {
		return err
	}
	grant := CreditGrant{ID: id, Team: team, Kind: CreditGrantPaid, AmountMicro: checkout.result.AmountMicro, Reference: "payment:" + string(event.Mode) + ":" + checkout.result.CheckoutID, CreatedBy: "payment_provider", CreatedAt: now}
	if err := l.insertGrant(ctx, grant); err != nil {
		return err
	}
	if _, err := tx.execContext(ctx, "UPDATE credit_checkouts SET paid_at = ?, grant_id = ?, closed_at = COALESCE(closed_at,?), failure = '', refill_blocked = 0 WHERE team = ? AND id = ? AND payment_mode = ?", now.UnixNano(), id, now.UnixNano(), string(team), checkout.result.CheckoutID, string(event.Mode)); err != nil {
		return err
	}
	if checkout.kind == checkoutKindCheckout {
		if _, err := tx.execContext(ctx, `UPDATE credit_checkouts SET refill_blocked = 0 WHERE team = ? AND refill_blocked = 1`, string(team)); err != nil {
			return err
		}
	}
	checkout.result.GrantID, checkout.result.Granted = id, true
	threeDS := auditThreeDS(event.ThreeDS)
	switch threeDS {
	case "", "authenticated", "attempt_acknowledged", "failed", "processing_error", "exempted", "not_supported", "unknown":
	default:
		threeDS = "unknown"
	}
	return recordGrantEventTx(ctx, tx, grant, auditActor{system: auditPaymentProvider}, auditGrant{SessionID: digestAudit(event.CheckoutID), ThreeDS: threeDS, LiabilityShift: event.LiabilityShift, Refill: checkout.kind == checkoutKindRefill})
}

func closeUnpaidTx(ctx context.Context, tx *storeTx, event capability.PaymentEvent, checkout *paymentCheckout) error {
	team, now := checkout.result.Team, tx.now(ctx)
	failure := ""
	kind := businessEventCheckoutFailed
	switch {
	case checkout.kind == checkoutKindRefill:
		failure, kind = cmp.Or(event.DeclineCode, "payment_failed"), businessEventRefillFailed
		checkout.result.RefillFailed = failure
	case event.Kind == capability.PaymentCheckoutExpired:
		kind = businessEventCheckoutExpired
	}
	// A successful Add funds supersedes failures from earlier refill reservations, including delayed outcomes.
	if _, err := tx.execContext(ctx, `UPDATE credit_checkouts SET closed_at = ?, failure = ?,
	    refill_blocked = CASE WHEN kind = 'refill' AND payment_method = (SELECT card_payment_method FROM teams WHERE name = credit_checkouts.team)
	      AND NOT EXISTS (SELECT 1 FROM credit_checkouts recovery WHERE recovery.team = credit_checkouts.team
	        AND recovery.kind = 'checkout' AND recovery.paid_at > credit_checkouts.opened_at) THEN 1 ELSE 0 END
	    WHERE team = ? AND id = ? AND payment_mode = ?`, now.UnixNano(), failure, string(team), checkout.result.CheckoutID, string(event.Mode)); err != nil {
		return err
	}
	return tx.operator.recordAudit(ctx, tx, auditContext{At: now, Team: team, Actor: auditActor{system: auditPaymentProvider}}, auditCheckout{CheckoutID: checkout.result.CheckoutID, Outcome: auditCheckoutOutcome(kind), PaymentMode: auditPaymentMode(event.Mode), EventID: digestAudit(event.EventID)})
}

// safety: a refused Add funds or refill payment closes its row without a grant and is queued for a full refund, whose
// settlement finds the team through the checkout's payment; a warned card also holds the team. Refusal removes the
// matching saved card, and a refused refill records its failure, so later passes cannot charge it again.
func refuseCheckoutTx(ctx context.Context, tx *storeTx, event capability.PaymentEvent, checkout *paymentCheckout, refusal cardRefusal) error {
	team, now := checkout.result.Team, tx.now(ctx)
	checkout.result.Refused = refusal.reason
	if _, err := tx.execContext(ctx, `UPDATE credit_checkouts SET closed_at = COALESCE(closed_at, ?), failure = ?, warning_id=?,
	    refill_blocked = CASE WHEN kind = 'refill' AND payment_method = (SELECT card_payment_method FROM teams WHERE name = credit_checkouts.team) THEN 1 ELSE 0 END
	    WHERE team = ? AND id = ? AND payment_mode = ?`, now.UnixNano(), refusal.reason, refusal.warning, string(team), checkout.result.CheckoutID, string(event.Mode)); err != nil {
		return err
	}
	if checkout.kind == checkoutKindRefill {
		checkout.result.RefillFailed = refusal.reason
	}
	if err := forgetTeamCardTx(ctx, tx, team, event.Fingerprint, refusal.reason, now); err != nil {
		return err
	}
	if err := tx.operator.recordAudit(ctx, tx, auditContext{Team: team, Actor: auditActor{system: auditPaymentProvider}, At: now}, auditCard{Reference: digestAudit(checkout.result.CheckoutID), Operation: businessEventCardRefused}); err != nil {
		return err
	}
	return queueCardRefundTx(ctx, tx, team, event.PaymentID, event.AmountMinor, refusal.reason, now)
}

// DeploymentID returns this store's payment deployment identity, created once at first use and never changed by
// configuration, so renaming the controller's host cannot turn its own in-flight payments foreign.
func (o *Operator) DeploymentID(ctx context.Context) (string, error) {
	var id string
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		fresh, err := newCreditID("dep")
		if err != nil {
			return err
		}
		data, err := json.Marshal(fresh)
		if err != nil {
			return err
		}
		if _, err := tx.execContext(ctx, `INSERT INTO settings(key,value) VALUES ('payment_deployment',?) ON CONFLICT(key) DO NOTHING`, data); err != nil {
			return err
		}
		var encoded []byte
		if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='payment_deployment'`).Scan(&encoded); err != nil {
			return err
		}
		return json.Unmarshal(encoded, &id)
	})
	return id, err
}
