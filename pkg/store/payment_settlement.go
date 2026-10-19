package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// SetPaymentMode configures the provider mode used for new reservations and settlement.
func (o *Operator) SetPaymentMode(ctx context.Context, mode capability.PaymentMode) error {
	if mode != capability.PaymentTest && mode != capability.PaymentLive {
		return ErrInvalidInput
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		if err := lockCreditLedgerTx(ctx, tx); err != nil {
			return err
		}
		data, err := json.Marshal(mode)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES ('payment_mode',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, data)
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
	// Warned reports a payment by a card under an actionable early fraud warning, which the operator is alerted to.
	Warned *WarnedCardPayment
	// Refused names why the payment was queued for a full refund, or is empty.
	Refused string
}

// SettlePayment settles verified provider events against committed billing facts.
func (s *Store) SettlePayment(ctx context.Context, proof capability.VerifiedPaymentEvent) (PaymentSettlement, error) {
	if proof == (capability.VerifiedPaymentEvent{}) {
		return PaymentSettlement{}, authorityError(apierr.Unauthenticated, "payment verification is required")
	}
	return s.settlePaymentFacts(ctx, proof.Event())
}

type paymentCheckout struct {
	result            PaymentSettlement
	payment, currency string
	amount            int64
	paid, closed      sql.NullInt64
	grant             sql.NullString
}

func (s *Store) settlePaymentFacts(ctx context.Context, event capability.PaymentEvent) (PaymentSettlement, error) {
	if event.Kind != capability.PaymentCheckoutPaid && event.Kind != capability.PaymentCheckoutExpired && event.Kind != capability.PaymentCheckoutFailed {
		return s.settleProviderPaymentFacts(ctx, event)
	}
	if event.EventID == "" || event.CheckoutID == "" || event.AmountMinor <= 0 || event.Currency == "" || (event.Mode != capability.PaymentTest && event.Mode != capability.PaymentLive) {
		return PaymentSettlement{}, authorityError(apierr.BadRequest, "invalid verified payment facts")
	}
	if event.Kind != capability.PaymentCheckoutPaid && event.Kind != capability.PaymentCheckoutExpired && event.Kind != capability.PaymentCheckoutFailed {
		return PaymentSettlement{}, authorityError(apierr.BadRequest, "unsupported payment outcome")
	}
	if event.Kind == capability.PaymentCheckoutPaid && event.PaymentID == "" {
		return PaymentSettlement{}, authorityError(apierr.BadRequest, "paid checkout has no payment identity")
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return PaymentSettlement{}, err
	}
	defer rollbackOrLog(tx)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return PaymentSettlement{}, err
	}
	if err := configuredPaymentModeTx(ctx, tx, event.Mode); err != nil {
		return PaymentSettlement{}, err
	}
	var checkout paymentCheckout
	err = tx.QueryRowContext(ctx, `SELECT id,team,payment_id,currency,amount_minor,amount_micro,paid_at,closed_at,grant_id FROM credit_checkouts WHERE payment_mode = ? AND session_id = ? AND session_id <> id`+tx.forUpdate(), string(event.Mode), event.CheckoutID).Scan(&checkout.result.CheckoutID, &checkout.result.Team, &checkout.payment, &checkout.currency, &checkout.amount, &checkout.result.AmountMicro, &checkout.paid, &checkout.closed, &checkout.grant)
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
		grant, found, err := paidGrantForPaymentTx(ctx, tx, event.PaymentID)
		if err != nil {
			return PaymentSettlement{}, err
		}
		if found && (grant.Team != checkout.result.Team || grant.ID != checkout.grant.String) {
			return PaymentSettlement{}, errors.Join(ErrCreditGrantConflict, authorityError(apierr.Conflict, "payment identity already funded another grant"))
		}
	}
	digest, err := paymentEventDigest(event)
	if err != nil {
		return PaymentSettlement{}, err
	}
	replayed, err := paymentEventReplayTx(ctx, tx, event, digest)
	if err != nil {
		return PaymentSettlement{}, err
	}
	checkout.result.GrantID, checkout.result.Payment = checkout.grant.String, event.PaymentID
	checkout.result.Replayed = replayed || checkout.paid.Valid
	if replayed {
		return checkout.result, tx.Commit()
	}
	if err := applyPaymentOutcomeTx(ctx, tx, event, &checkout); err != nil {
		return PaymentSettlement{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO payment_events(team,payment_mode,event_id,checkout_id,payment_id,event_kind,event_digest,grant_id,processed_at) VALUES (?,?,?,?,?,?,?,?,?)`, string(checkout.result.Team), string(event.Mode), event.EventID, checkout.result.CheckoutID, event.PaymentID, string(event.Kind), digest, optionalString(checkout.result.GrantID), tx.now().UnixNano())
	if err != nil {
		return PaymentSettlement{}, err
	}
	if err := tx.Commit(); err != nil {
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

func applyPaymentOutcomeTx(ctx context.Context, tx *storeTx, event capability.PaymentEvent, checkout *paymentCheckout) error {
	now := tx.now().UnixNano()
	if checkout.payment == "" && event.PaymentID != "" {
		if _, err := tx.ExecContext(ctx, "UPDATE credit_checkouts SET payment_id = ? WHERE team = ? AND id = ? AND payment_mode = ?", event.PaymentID, string(checkout.result.Team), checkout.result.CheckoutID, string(event.Mode)); err != nil {
			return err
		}
	}
	if checkout.paid.Valid {
		return nil
	}
	if event.Kind != capability.PaymentCheckoutPaid {
		if checkout.closed.Valid {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE credit_checkouts SET closed_at = ? WHERE team = ? AND id = ? AND payment_mode = ?", now, string(checkout.result.Team), checkout.result.CheckoutID, string(event.Mode)); err != nil {
			return err
		}
		kind := BusinessEventCheckoutFailed
		if event.Kind == capability.PaymentCheckoutExpired {
			kind = BusinessEventCheckoutExpired
		}
		return RecordBusinessEvent(tx, BusinessEvent{At: tx.now(), Team: checkout.result.Team, Kind: kind, SubjectID: checkout.result.CheckoutID, Actor: "payment_provider", Attrs: map[string]any{"session_id": event.CheckoutID, "payment_mode": event.Mode, "event_id": event.EventID}})
	}
	team := checkout.result.Team
	refusal, err := refuseCardTx(ctx, tx, team, event.PaymentID, event.Fingerprint)
	if err != nil {
		return err
	}
	if refusal.reason != "" {
		return refuseCheckoutTx(ctx, tx, event, checkout, refusal)
	}
	grant, err := newCreditID("paid")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO credit_grants(id,team,kind,amount_micro,reference,created_by,created_at) VALUES (?,?,'paid',?,?,?,?)`, grant, string(checkout.result.Team), checkout.result.AmountMicro, "payment:"+string(event.Mode)+":"+checkout.result.CheckoutID, "payment_provider", now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE credit_checkouts SET paid_at = ?, grant_id = ?, closed_at = COALESCE(closed_at,?), three_d_secure = ? WHERE team = ? AND id = ? AND payment_mode = ?", now, grant, now, event.ThreeDS, string(checkout.result.Team), checkout.result.CheckoutID, string(event.Mode))
	if err != nil {
		return err
	}
	checkout.result.GrantID = grant
	checkout.result.Granted = true
	if err := pinCardTx(ctx, tx, team, event.PaymentID, event.Fingerprint, tx.now()); err != nil {
		return err
	}
	if err := applyPaymentRefundsTx(ctx, tx, event.PaymentID); err != nil {
		return err
	}
	if err := recordGrantEventTx(tx, CreditGrant{ID: grant, Team: checkout.result.Team, Kind: CreditGrantPaid, AmountMicro: checkout.result.AmountMicro, Reference: "payment:" + string(event.Mode) + ":" + checkout.result.CheckoutID, CreatedBy: "payment_provider", CreatedAt: tx.now()}, map[string]any{"session_id": event.CheckoutID, "three_d_secure": event.ThreeDS}); err != nil {
		return err
	}
	room, err := headroomTx(ctx, tx, checkout.result.Team, tx.now())
	if err != nil {
		return err
	}
	if room > 0 {
		return clearTeamCreditExhaustedTx(ctx, tx, checkout.result.Team, now)
	}
	return nil
}

// safety: a refused Add funds payment closes its checkout without a grant and is queued for a full refund, whose
// settlement finds the team through the checkout's payment; a warned card also holds the team.
func refuseCheckoutTx(ctx context.Context, tx *storeTx, event capability.PaymentEvent, checkout *paymentCheckout, refusal cardRefusal) error {
	team, now := checkout.result.Team, tx.now()
	checkout.result.Refused = refusal.reason
	if _, err := tx.ExecContext(ctx, "UPDATE credit_checkouts SET closed_at = COALESCE(closed_at, ?) WHERE team = ? AND id = ? AND payment_mode = ?", now.UnixNano(), string(team), checkout.result.CheckoutID, string(event.Mode)); err != nil {
		return err
	}
	if refusal.warning != "" {
		if err := holdForWarningTx(ctx, tx, team, refusal.warning, event.PaymentID, now); err != nil {
			return err
		}
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventCardRefused, SubjectID: checkout.result.CheckoutID, Actor: "payment_provider", At: now,
		Attrs: map[string]any{"reason": refusal.reason, "payment_id": event.PaymentID, "warning_id": refusal.warning},
	}); err != nil {
		return err
	}
	return queueCardRefundTx(ctx, tx, team, CardPayment{PaymentIntent: event.PaymentID, AmountCents: event.AmountMinor}, refusal.reason, now)
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES ('payment_deployment',?) ON CONFLICT(key) DO NOTHING`, data); err != nil {
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
